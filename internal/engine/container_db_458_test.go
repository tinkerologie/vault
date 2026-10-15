package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

var (
	pgEnv458    = []string{"POSTGRES_USER=app", "POSTGRES_PASSWORD=secret"}
	mysqlBad458 = []string{"MYSQL_USER=app"} // no root password, no database
)

func TestDumpDatabase458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		image       string
		env         []string
		compression string
		script      execScript
		missingDir  bool
		wantErr     string
		wantNil     bool
	}{
		{name: "not a database", image: "nginx", wantNil: true},
		{name: "unusable credentials", image: "mysql:8", env: mysqlBad458, wantErr: "cannot dump mysql database"},
		{name: "plain dump", image: "postgres:16", env: pgEnv458, script: execScript{output: muxFrame(1, "CREATE TABLE t();\n")}},
		{name: "gzip dump", image: "postgres:16", env: pgEnv458, compression: CompressionGzip, script: execScript{output: muxFrame(1, "CREATE TABLE t();\n")}},
		{name: "exec failure", image: "postgres:16", env: pgEnv458, script: execScript{exitCode: 1, output: muxFrame(2, "denied")}, wantErr: "dumping postgres database for db"},
		{name: "empty dump", image: "postgres:16", env: pgEnv458, compression: CompressionZstd, script: execScript{}, wantErr: "the dump was empty"},
		{name: "unwritable destination", image: "postgres:16", env: pgEnv458, missingDir: true, wantErr: "creating database dump file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeDocker458{}
			rec := installExec(f, tt.script)
			h := &ContainerHandler{cli: f}
			dest := t.TempDir()
			if tt.missingDir {
				dest = filepath.Join(dest, "missing")
			}
			bf, err := h.dumpDatabase(t.Context(), "cid", "db", tt.image, tt.env, dest, tt.compression, noopProgress)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				// A failed dump must not leave a partial file behind.
				if _, statErr := os.Stat(filepath.Join(dest, DatabaseDumpFile+archiveExt(tt.compression))); statErr == nil {
					t.Error("partial dump file left behind")
				}
				return
			}
			if err != nil {
				t.Fatalf("dumpDatabase: %v", err)
			}
			if tt.wantNil {
				if bf != nil || rec.createCount() != 0 {
					t.Fatalf("expected no dump, got %v (execs=%d)", bf, rec.createCount())
				}
				return
			}
			if bf == nil || bf.Size == 0 || bf.Name != DatabaseDumpFile+archiveExt(tt.compression) {
				t.Fatalf("unexpected backup file %+v", bf)
			}
			if got := rec.lastCreate().Env; len(got) != 1 || got[0] != "PGPASSWORD=secret" {
				t.Errorf("password not passed via env: %v", got)
			}
		})
	}
}

func gzipBytes458(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRestoreDatabase458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		image     string
		dump      []byte // nil: file absent
		gzipped   string // when set, the dump is this text gzip-compressed
		script    execScript
		wantErr   string
		wantStdin string
	}{
		{name: "not a database", image: "redis"},
		{name: "missing dump", image: "postgres", wantErr: "opening database dump"},
		{name: "truncated gzip", image: "postgres", dump: []byte{0x1f, 0x8b}, wantErr: "decompressing database dump"},
		{name: "gzip dump reloaded via stdin", image: "postgres", gzipped: "INSERT 1;\n", script: execScript{halfClose: true}, wantStdin: "INSERT 1;\n"},
		{name: "plain dump into mariadb", image: "mariadb:11", dump: []byte("INSERT 2;\n"), script: execScript{halfClose: true}, wantStdin: "INSERT 2;\n"},
		{name: "reload failure", image: "mysql:8", dump: []byte("x"), script: execScript{exitCode: 1, output: muxFrame(2, "syntax")}, wantErr: "reloading mysql dump for db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeDocker458{}
			rec := installExec(f, tt.script)
			h := &ContainerHandler{cli: f}
			dumpPath := filepath.Join(t.TempDir(), "database.sql.gz")
			data := tt.dump
			if tt.gzipped != "" {
				data = gzipBytes458(t, tt.gzipped)
			}
			if data != nil {
				if err := os.WriteFile(dumpPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"POSTGRES_PASSWORD=pw", "MYSQL_ROOT_PASSWORD=pw", "MARIADB_ROOT_PASSWORD=pw"}
			err := h.restoreDatabase(t.Context(), "cid", "db", tt.image, env, dumpPath)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("restoreDatabase: %v", err)
			}
			if tt.wantStdin != "" {
				if got := rec.lastConn().stdinString(); got != tt.wantStdin {
					t.Errorf("stdin = %q, want %q", got, tt.wantStdin)
				}
			} else if rec.createCount() != 0 {
				t.Error("exec ran for a non-database container")
			}
		})
	}
}

func TestDumpDatabaseToTemp458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		image   string
		env     []string
		script  execScript
		wantErr string
		wantOut string
	}{
		{name: "not a database", image: "alpine"},
		{name: "unusable credentials", image: "mariadb", env: mysqlBad458, wantErr: "cannot dump mariadb database"},
		{name: "dump written", image: "postgres", env: pgEnv458, script: execScript{output: muxFrame(1, "-- dump\n")}, wantOut: "-- dump\n"},
		{name: "exec failure", image: "postgres", env: pgEnv458, script: execScript{createErr: errFake458}, wantErr: "creating exec"},
		{name: "empty dump", image: "postgres", env: pgEnv458, script: execScript{}, wantErr: "the dump was empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeDocker458{}
			installExec(f, tt.script)
			h := &ContainerHandler{cli: f}
			path, cleanup, err := h.dumpDatabaseToTemp(t.Context(), "cid", "db", tt.image, tt.env)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("dumpDatabaseToTemp: %v", err)
			}
			if tt.wantOut == "" {
				if path != "" || cleanup != nil {
					t.Fatalf("expected no dump, got %q", path)
				}
				return
			}
			got, readErr := os.ReadFile(path) // #nosec G304 -- test temp file
			if readErr != nil || string(got) != tt.wantOut {
				t.Fatalf("dump = %q, %v", got, readErr)
			}
			cleanup()
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("cleanup left %s behind", path)
			}
		})
	}
}

func TestWaitForDatabaseReady458(t *testing.T) {
	t.Parallel()

	t.Run("not a database", func(t *testing.T) {
		t.Parallel()
		h := &ContainerHandler{cli: &fakeDocker458{}}
		if err := h.waitForDatabaseReady(t.Context(), "cid", "busybox", nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ready on first probe", func(t *testing.T) {
		t.Parallel()
		f := &fakeDocker458{}
		rec := installExec(f, execScript{})
		h := &ContainerHandler{cli: f}
		if err := h.waitForDatabaseReady(t.Context(), "cid", "mysql:8", []string{"MYSQL_ROOT_PASSWORD=pw"}); err != nil {
			t.Fatal(err)
		}
		if got := rec.lastCreate().Env; len(got) != 1 || got[0] != "MYSQL_PWD=pw" {
			t.Errorf("probe env = %v", got)
		}
	})

	t.Run("already cancelled", func(t *testing.T) {
		t.Parallel()
		f := &fakeDocker458{}
		rec := installExec(f, execScript{})
		h := &ContainerHandler{cli: f}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := h.waitForDatabaseReady(ctx, "cid", "postgres", pgEnv458); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if rec.createCount() != 0 {
			t.Error("probe ran on a cancelled context")
		}
	})

	t.Run("cancelled while waiting to retry", func(t *testing.T) {
		t.Parallel()
		f := &fakeDocker458{}
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		f.execCreate = func(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
			calls++
			cancel() // server not up yet, and the restore is abandoned
			return client.ExecCreateResult{}, errFake458
		}
		h := &ContainerHandler{cli: f}
		if err := h.waitForDatabaseReady(ctx, "cid", "postgres", pgEnv458); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Errorf("probe calls = %d, want 1", calls)
		}
	})
}

func TestChunkFileIntoRepoErrors458(t *testing.T) {
	t.Parallel()
	repo := newChunkTestRepo(t)
	if _, err := chunkFileIntoRepo(repo, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("expected stat error for a missing file")
	}
	// A directory stats fine but cannot be read as a stream.
	if _, err := chunkFileIntoRepo(repo, t.TempDir()); err == nil {
		t.Error("expected read error for a directory")
	}
}

// A probe that prints output (mysqladmin ping) is still a success.
func TestWaitForDatabaseReadyIgnoresProbeOutput458(t *testing.T) {
	t.Parallel()
	f := &fakeDocker458{}
	installExec(f, execScript{output: muxFrame(1, "mysqld is alive\n")})
	h := &ContainerHandler{cli: f}
	if err := h.waitForDatabaseReady(t.Context(), "cid", "mariadb", []string{"MARIADB_ROOT_PASSWORD=pw"}); err != nil {
		t.Fatal(err)
	}
}

func TestDetectDatabaseByImage458(t *testing.T) {
	t.Parallel()
	for image, want := range map[string]DatabaseKind{
		"mariadb:11":              DatabaseMariaDB,
		"ghcr.io/x/postgres:16":   DatabasePostgres,
		"MySQL:8":                 DatabaseMySQL,
		"linuxserver/plex:latest": DatabaseNone,
	} {
		if got := DetectDatabaseByImage(image); got != want {
			t.Errorf("DetectDatabaseByImage(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestDatabaseCredentialsEdgeCases458(t *testing.T) {
	t.Parallel()
	// Entries without "=" are ignored rather than matched by name.
	got := databaseCredentials(DatabasePostgres, []string{"POSTGRES_USER", "POSTGRES_PASSWORD=pw"})
	if got.User != "postgres" || got.Password != "pw" {
		t.Errorf("postgres creds = %+v", got)
	}
	if got := databaseCredentials(DatabaseNone, []string{"POSTGRES_USER=x"}); got != (dbCredentials{}) {
		t.Errorf("unknown kind creds = %+v", got)
	}
}

// Not parallel: points TMPDIR at a missing directory.
func TestDumpDatabaseToTempNoTempDir458(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	f := &fakeDocker458{}
	rec := installExec(f, execScript{output: muxFrame(1, "x")})
	_, _, err := (&ContainerHandler{cli: f}).dumpDatabaseToTemp(t.Context(), "cid", "db", "postgres", pgEnv458)
	if err == nil || !strings.Contains(err.Error(), "creating database dump temp file") {
		t.Fatalf("err = %v", err)
	}
	if rec.createCount() != 0 {
		t.Error("dump ran without a temp file to write to")
	}
}
