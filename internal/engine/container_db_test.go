package engine

import (
	"bytes"
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

// TestRootDumpCoversWholeServer: a per-database dump omits users, roles and
// grants, so where privileges allow it the whole server is taken.
func TestRootDumpCoversWholeServer(t *testing.T) {
	pg, _ := dumpCommand(DatabasePostgres, dbCredentials{User: "postgres", IsRoot: true})
	if pg[0] != "pg_dumpall" {
		t.Errorf("postgres should use pg_dumpall, got %v", pg)
	}
	for _, kind := range []DatabaseKind{DatabaseMySQL, DatabaseMariaDB} {
		cmd, _ := dumpCommand(kind, dbCredentials{User: "root", IsRoot: true})
		if !strings.Contains(strings.Join(cmd, " "), "--all-databases") {
			t.Errorf("%s should dump all databases as root, got %v", kind, cmd)
		}
	}
}

// TestNonRootDumpIsScopedAndUnprivileged reflects what a live MySQL container
// actually rejected: --all-databases needs privileges an application user
// lacks, and the FLUSH TABLES that --single-transaction issues needs RELOAD.
func TestNonRootDumpIsScopedAndUnprivileged(t *testing.T) {
	for _, kind := range []DatabaseKind{DatabaseMySQL, DatabaseMariaDB} {
		cmd, _ := dumpCommand(kind, dbCredentials{User: "app", Database: "appdb"})
		joined := strings.Join(cmd, " ")
		if strings.Contains(joined, "--all-databases") {
			t.Errorf("%s: non-root dump must not attempt every database: %v", kind, cmd)
		}
		if !strings.Contains(joined, "--databases appdb") {
			t.Errorf("%s: non-root dump should be scoped to the configured database: %v", kind, cmd)
		}
		if strings.Contains(joined, "--single-transaction") {
			t.Errorf("%s: non-root dump must not use --single-transaction (needs RELOAD): %v", kind, cmd)
		}
		if !strings.Contains(joined, "--no-tablespaces") {
			t.Errorf("%s: non-root dump should avoid needing PROCESS: %v", kind, cmd)
		}
	}
}

// TestRandomRootPasswordIsNotTrusted covers the trap that broke live MariaDB
// and MySQL containers: with *_RANDOM_ROOT_PASSWORD set the image generates
// root's password at initialisation and IGNORES the supplied ROOT_PASSWORD,
// which is nonetheless still present in the environment.
func TestRandomRootPasswordIsNotTrusted(t *testing.T) {
	c := databaseCredentials(DatabaseMariaDB, []string{
		"MARIADB_ROOT_PASSWORD=decoy",
		"MARIADB_RANDOM_ROOT_PASSWORD=yes",
		"MARIADB_USER=app", "MARIADB_PASSWORD=apppw", "MARIADB_DATABASE=appdb",
	})
	if c.IsRoot {
		t.Fatalf("root credentials trusted despite a randomly generated password: %+v", c)
	}
	if c.User != "app" || c.Password != "apppw" {
		t.Fatalf("should fall back to the application user, got %+v", c)
	}

	// MySQL's variant, where ROOT_PASSWORD is present but empty.
	m := databaseCredentials(DatabaseMySQL, []string{
		"MYSQL_ROOT_PASSWORD=",
		"MYSQL_RANDOM_ROOT_PASSWORD=yes",
		"MYSQL_USER=app", "MYSQL_PASSWORD=apppw",
	})
	if m.IsRoot || m.User != "app" {
		t.Fatalf("got %+v, want the application user", m)
	}
}

// TestRootIsUsedWhenTheRootPasswordIsReal keeps the preferred path intact.
func TestRootIsUsedWhenTheRootPasswordIsReal(t *testing.T) {
	c := databaseCredentials(DatabaseMySQL, []string{"MYSQL_ROOT_PASSWORD=real", "MYSQL_USER=app", "MYSQL_PASSWORD=a"})
	if !c.IsRoot || c.User != "root" || c.Password != "real" {
		t.Fatalf("got %+v, want root", c)
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

// TestCountingWriterDetectsEmptyOutput backs the empty-dump guard.
//
// The guard cannot use the file size: closing an empty gzip or zstd writer
// still emits a header frame, so a command that exits 0 having produced no
// output would leave a non-zero file and be accepted as a valid dump.
func TestCountingWriterDetectsEmptyOutput(t *testing.T) {
	for _, compression := range []string{"", "gzip", "zstd"} {
		var buf bytes.Buffer
		cw, closeCompress, err := compressedWriter(&buf, compression)
		if err != nil {
			t.Fatalf("%s: %v", compression, err)
		}
		counted := &dumpByteCounter{w: cw}
		// Write nothing, as a failed dump command would.
		if err := closeCompress(); err != nil {
			t.Fatalf("%s: %v", compression, err)
		}

		if counted.n != 0 {
			t.Errorf("%s: counted %d bytes of output, want 0", compression, counted.n)
		}
		if compression != "" && buf.Len() == 0 {
			t.Errorf("%s: expected a non-empty compressed frame — otherwise this test proves nothing", compression)
		}
	}
}

func TestCountingWriterCountsRealOutput(t *testing.T) {
	var buf bytes.Buffer
	c := &dumpByteCounter{w: &buf}
	if _, err := c.Write([]byte("SELECT 1;")); err != nil {
		t.Fatal(err)
	}
	if c.n != 9 {
		t.Fatalf("counted %d, want 9", c.n)
	}
}

// TestPostgresRestoreStopsOnError: without ON_ERROR_STOP psql continues past
// failing statements and exits 0, so a half-applied reload reports success.
func TestPostgresRestoreStopsOnError(t *testing.T) {
	cmd, _ := restoreCommand(DatabasePostgres, dbCredentials{User: "postgres"})
	if !strings.Contains(strings.Join(cmd, " "), "ON_ERROR_STOP=1") {
		t.Fatalf("psql must stop on the first error, got %v", cmd)
	}
}

// TestValidateDumpCredentials rejects combinations that cannot produce a dump,
// so the failure names the missing configuration rather than surfacing a raw
// driver error.
func TestValidateDumpCredentials(t *testing.T) {
	if err := validateDumpCredentials(DatabaseMySQL, dbCredentials{}); err == nil {
		t.Error("no user at all should be rejected")
	}
	// The live-observed shape: random root password, so no root, and no
	// database named to scope a non-root dump to.
	if err := validateDumpCredentials(DatabaseMySQL, dbCredentials{User: "app"}); err == nil {
		t.Error("non-root with no database should be rejected")
	}
	if err := validateDumpCredentials(DatabaseMySQL, dbCredentials{User: "app", Database: "appdb"}); err != nil {
		t.Errorf("non-root with a database is valid: %v", err)
	}
	if err := validateDumpCredentials(DatabaseMySQL, dbCredentials{User: "root", IsRoot: true}); err != nil {
		t.Errorf("root needs no database: %v", err)
	}
	if err := validateDumpCredentials(DatabasePostgres, dbCredentials{User: "postgres", IsRoot: true}); err != nil {
		t.Errorf("postgres superuser is valid: %v", err)
	}
}

// TestContainerStateDescription distinguishes a crash loop from a plain stop,
// because the remedy differs.
func TestContainerStateDescription(t *testing.T) {
	if got := containerStateDescription("restarting", true); !strings.Contains(got, "crash loop") {
		t.Errorf("got %q, want a crash-loop hint", got)
	}
	if got := containerStateDescription("exited", false); got != "exited" {
		t.Errorf("got %q, want the plain status", got)
	}
	if got := containerStateDescription("", false); got != "not running" {
		t.Errorf("got %q, want a fallback", got)
	}
}
