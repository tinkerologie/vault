package engine

import (
	"strings"
	"testing"
)

// TestDetectDatabase requires image AND environment to agree (issue #259).
func TestDetectDatabase(t *testing.T) {
	cases := []struct {
		name  string
		image string
		env   []string
		want  DatabaseKind
	}{
		{"postgres", "postgres:alpine", []string{"POSTGRES_PASSWORD=x"}, DatabasePostgres},
		{"mysql", "mysql", []string{"MYSQL_ROOT_PASSWORD=x"}, DatabaseMySQL},
		{"mariadb", "mariadb", []string{"MARIADB_ROOT_PASSWORD=x"}, DatabaseMariaDB},
		{
			// The mariadb image accepts MYSQL_* for compatibility, so checking
			// MySQL first would reach for mysqldump instead of mariadb-dump.
			name:  "mariadb configured with MYSQL_ vars is still mariadb",
			image: "mariadb:11", env: []string{"MYSQL_ROOT_PASSWORD=x"}, want: DatabaseMariaDB,
		},
		{"private registry mirror", "registry.local/library/postgres:16", []string{"POSTGRES_USER=app"}, DatabasePostgres},
		{
			// A stray variable pointing at a server elsewhere must not make an
			// app container look like a database — the dump command would fail.
			name:  "app container with a stray MYSQL_ var",
			image: "nextcloud", env: []string{"MYSQL_HOST=db"}, want: DatabaseNone,
		},
		{"database image with no corroborating env", "postgres:alpine", nil, DatabaseNone},
		{"unrelated container", "nginx", []string{"PATH=/usr/bin"}, DatabaseNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectDatabase(tc.image, tc.env); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectDatabaseByImageIsAdvisory: discovery has no environment, so the
// hint is image-only and deliberately looser than DetectDatabase.
func TestDetectDatabaseByImageIsAdvisory(t *testing.T) {
	if got := DetectDatabaseByImage("postgres:alpine"); got != DatabasePostgres {
		t.Fatalf("got %q, want postgres", got)
	}
	// Looser than the authoritative check: no env needed.
	if DetectDatabase("postgres:alpine", nil) != DatabaseNone {
		t.Fatal("authoritative detection should still require env")
	}
	if got := DetectDatabaseByImage("nginx"); got != DatabaseNone {
		t.Fatalf("got %q, want none", got)
	}
}

func TestDatabaseCredentials(t *testing.T) {
	t.Run("postgres defaults to the image superuser", func(t *testing.T) {
		c := databaseCredentials(DatabasePostgres, []string{"POSTGRES_PASSWORD=secret"})
		if c.User != "postgres" || c.Password != "secret" {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("postgres honours an explicit user", func(t *testing.T) {
		c := databaseCredentials(DatabasePostgres, []string{"POSTGRES_USER=app", "POSTGRES_PASSWORD=s"})
		if c.User != "app" {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("mysql prefers root over an application user", func(t *testing.T) {
		// A dump taken as an app user silently omits databases it cannot see,
		// which would look like a successful backup of an incomplete server.
		c := databaseCredentials(DatabaseMySQL, []string{"MYSQL_USER=app", "MYSQL_PASSWORD=a", "MYSQL_ROOT_PASSWORD=r"})
		if c.User != "root" || c.Password != "r" {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("mysql falls back to the application user", func(t *testing.T) {
		c := databaseCredentials(DatabaseMySQL, []string{"MYSQL_USER=app", "MYSQL_PASSWORD=a"})
		if c.User != "app" || c.Password != "a" {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("mariadb variables are accepted", func(t *testing.T) {
		c := databaseCredentials(DatabaseMariaDB, []string{"MARIADB_ROOT_PASSWORD=r"})
		if c.User != "root" || c.Password != "r" {
			t.Fatalf("got %+v", c)
		}
	})
}

// TestDumpCommandKeepsPasswordsOutOfArgv is a security property: anything on
// the command line is readable in the container's own process list by every
// other process in it.
func TestDumpCommandKeepsPasswordsOutOfArgv(t *testing.T) {
	const secret = "sup3rs3cret"
	for _, kind := range []DatabaseKind{DatabasePostgres, DatabaseMySQL, DatabaseMariaDB} {
		cmd, env := dumpCommand(kind, dbCredentials{User: "root", Password: secret})
		if strings.Contains(strings.Join(cmd, " "), secret) {
			t.Errorf("%s: password leaked into argv: %v", kind, cmd)
		}
		if !strings.Contains(strings.Join(env, " "), secret) {
			t.Errorf("%s: password not passed via env: %v", kind, env)
		}
	}
}

func TestRestoreCommandKeepsPasswordsOutOfArgv(t *testing.T) {
	const secret = "sup3rs3cret"
	for _, kind := range []DatabaseKind{DatabasePostgres, DatabaseMySQL, DatabaseMariaDB} {
		cmd, env := restoreCommand(kind, dbCredentials{User: "root", Password: secret})
		if strings.Contains(strings.Join(cmd, " "), secret) {
			t.Errorf("%s: password leaked into argv: %v", kind, cmd)
		}
		if !strings.Contains(strings.Join(env, " "), secret) {
			t.Errorf("%s: password not passed via env: %v", kind, env)
		}
	}
}

// TestDumpCommandCoversWholeServer: a per-database dump omits users, roles and
// grants, so a restore would come back missing the accounts the application
// authenticates with.
func TestDumpCommandCoversWholeServer(t *testing.T) {
	pg, _ := dumpCommand(DatabasePostgres, dbCredentials{User: "postgres"})
	if pg[0] != "pg_dumpall" {
		t.Errorf("postgres should use pg_dumpall, got %v", pg)
	}
	for _, kind := range []DatabaseKind{DatabaseMySQL, DatabaseMariaDB} {
		cmd, _ := dumpCommand(kind, dbCredentials{User: "root"})
		if !strings.Contains(strings.Join(cmd, " "), "--all-databases") {
			t.Errorf("%s should dump all databases, got %v", kind, cmd)
		}
	}
}

// TestMariaDBFallsBackToMysqldump: older mariadb images ship only mysqldump.
func TestMariaDBFallsBackToMysqldump(t *testing.T) {
	cmd, _ := dumpCommand(DatabaseMariaDB, dbCredentials{User: "root"})
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "mariadb-dump") || !strings.Contains(joined, "mysqldump") {
		t.Fatalf("expected a mariadb-dump/mysqldump fallback, got %v", cmd)
	}
}

// TestDatabaseDumpEnabledDefaultsOff: a dump runs commands inside a live
// container and costs extra space, so it is opted into.
func TestDatabaseDumpEnabledDefaultsOff(t *testing.T) {
	if databaseDumpEnabled(map[string]any{}) {
		t.Fatal("absent setting should mean off")
	}
	if databaseDumpEnabled(map[string]any{"database_dump": false}) {
		t.Fatal("explicit false should mean off")
	}
	if !databaseDumpEnabled(map[string]any{"database_dump": true}) {
		t.Fatal("explicit true should mean on")
	}
}

func TestUnknownKindProducesNoCommand(t *testing.T) {
	if cmd, _ := dumpCommand(DatabaseNone, dbCredentials{}); len(cmd) != 0 {
		t.Fatalf("got %v, want no command", cmd)
	}
	if cmd, _ := restoreCommand(DatabaseNone, dbCredentials{}); len(cmd) != 0 {
		t.Fatalf("got %v, want no command", cmd)
	}
}
