package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// DatabaseKind identifies a database server Vault knows how to dump.
type DatabaseKind string

const (
	DatabaseNone     DatabaseKind = ""
	DatabasePostgres DatabaseKind = "postgres"
	DatabaseMySQL    DatabaseKind = "mysql"
	DatabaseMariaDB  DatabaseKind = "mariadb"
)

// DatabaseDumpFile is the name written into the backup, before the job's
// compression extension is appended.
const DatabaseDumpFile = "database.sql"

// ContainerDBDumpKey is the manifest key a dedup/chunked container backup
// stores its logical dump under, alongside __vol__ volume entries.
const ContainerDBDumpKey = "__dbdump__"

// How long a restored database server is given to accept connections before
// the dump reload gives up, and how often it is polled.
const (
	databaseReadyTimeout = 2 * time.Minute
	databaseReadyPoll    = 2 * time.Second
)

// dbCredentials are the connection details recovered from a container's own
// environment. Vault deliberately stores no database passwords of its own: the
// container already holds what it needs to run, so reading it back means the
// feature works with no configuration and adds no new secret to protect.
type dbCredentials struct {
	User     string
	Password string
	Database string
}

// DetectDatabaseByImage is the image-only hint used by discovery, where the
// container list carries no environment and inspecting every container to get
// it would put N API calls behind a frequently-hit endpoint.
//
// Advisory on purpose: it decides only whether the job wizard OFFERS a dump.
// DetectDatabase, which also requires the environment to corroborate, is what
// the backup actually acts on — so an image that merely looks like a database
// costs a checkbox nobody ticks, not a failed backup.
func DetectDatabaseByImage(image string) DatabaseKind {
	lower := strings.ToLower(image)
	switch {
	case strings.Contains(lower, "mariadb"):
		return DatabaseMariaDB
	case strings.Contains(lower, "postgres"):
		return DatabasePostgres
	case strings.Contains(lower, "mysql"):
		return DatabaseMySQL
	}
	return DatabaseNone
}

// DetectDatabase identifies the database server a container runs, from its
// image name and environment.
//
// Both signals are used because neither alone is reliable: images get renamed
// or mirrored to private registries, and a non-database container can carry a
// stray MYSQL_* variable pointing at a server elsewhere. Requiring the
// environment to corroborate the image keeps false positives out — a false
// positive would run a dump command against something that is not a database
// and fail the backup.
func DetectDatabase(image string, env []string) DatabaseKind {
	lower := strings.ToLower(image)
	envHas := func(prefixes ...string) bool {
		for _, e := range env {
			for _, p := range prefixes {
				if strings.HasPrefix(e, p) {
					return true
				}
			}
		}
		return false
	}

	// MariaDB before MySQL: the mariadb image accepts MYSQL_* variables for
	// compatibility, so checking MySQL first would misidentify it and reach for
	// mysqldump when mariadb-dump is the correct tool.
	switch {
	case strings.Contains(lower, "mariadb") && envHas("MARIADB_", "MYSQL_"):
		return DatabaseMariaDB
	case strings.Contains(lower, "postgres") && envHas("POSTGRES_", "PG"):
		return DatabasePostgres
	case strings.Contains(lower, "mysql") && envHas("MYSQL_"):
		return DatabaseMySQL
	}
	return DatabaseNone
}

// databaseCredentials extracts connection details from the container's
// environment, applying each image's documented defaults when a variable is
// absent.
func databaseCredentials(kind DatabaseKind, env []string) dbCredentials {
	get := func(keys ...string) string {
		for _, e := range env {
			name, value, ok := strings.Cut(e, "=")
			if !ok {
				continue
			}
			for _, k := range keys {
				if name == k {
					return value
				}
			}
		}
		return ""
	}

	switch kind {
	case DatabasePostgres:
		user := get("POSTGRES_USER")
		if user == "" {
			user = "postgres" // the image's default superuser
		}
		return dbCredentials{
			User:     user,
			Password: get("POSTGRES_PASSWORD"),
			Database: get("POSTGRES_DB"),
		}
	case DatabaseMySQL, DatabaseMariaDB:
		// Prefer root: a dump taken as an application user silently omits every
		// database that user cannot see, which would look like a successful
		// backup of an incomplete server.
		if pw := get("MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"); pw != "" {
			return dbCredentials{User: "root", Password: pw, Database: get("MYSQL_DATABASE", "MARIADB_DATABASE")}
		}
		return dbCredentials{
			User:     get("MYSQL_USER", "MARIADB_USER"),
			Password: get("MYSQL_PASSWORD", "MARIADB_PASSWORD"),
			Database: get("MYSQL_DATABASE", "MARIADB_DATABASE"),
		}
	}
	return dbCredentials{}
}

// dumpCommand returns the command and environment for a logical dump.
//
// Passwords go through the environment, never argv: anything on the command
// line is visible in the container's own process list to every other process
// in it.
//
// The dump covers the whole server rather than a single database. A per-database
// dump omits users, roles, and grants, so a restore would come back missing the
// accounts the application authenticates with.
func dumpCommand(kind DatabaseKind, creds dbCredentials) (cmd []string, env []string) {
	switch kind {
	case DatabasePostgres:
		// --clean so a restore into a populated server replaces objects rather
		// than failing on every "already exists".
		cmd = []string{"pg_dumpall", "--clean", "-U", creds.User}
		if creds.Password != "" {
			env = append(env, "PGPASSWORD="+creds.Password)
		}
	case DatabaseMariaDB:
		// mariadb-dump is the current name; older images only ship mysqldump,
		// so fall back rather than failing on a still-supported image.
		cmd = []string{"sh", "-c",
			"if command -v mariadb-dump >/dev/null 2>&1; then exec mariadb-dump \"$@\"; else exec mysqldump \"$@\"; fi",
			"sh", "--all-databases", "--single-transaction", "--quick", "-u", creds.User}
		if creds.Password != "" {
			env = append(env, "MYSQL_PWD="+creds.Password)
		}
	case DatabaseMySQL:
		// --single-transaction takes a consistent snapshot without locking the
		// server for the duration; --quick streams rows instead of buffering a
		// whole table in memory.
		cmd = []string{"mysqldump", "--all-databases", "--single-transaction", "--quick", "-u", creds.User}
		if creds.Password != "" {
			env = append(env, "MYSQL_PWD="+creds.Password)
		}
	}
	return cmd, env
}

// restoreCommand returns the command and environment that reload a dump
// produced by dumpCommand, reading it from stdin.
func restoreCommand(kind DatabaseKind, creds dbCredentials) (cmd []string, env []string) {
	switch kind {
	case DatabasePostgres:
		cmd = []string{"psql", "-U", creds.User, "-d", "postgres"}
		if creds.Password != "" {
			env = append(env, "PGPASSWORD="+creds.Password)
		}
	case DatabaseMariaDB:
		cmd = []string{"sh", "-c",
			"if command -v mariadb >/dev/null 2>&1; then exec mariadb \"$@\"; else exec mysql \"$@\"; fi",
			"sh", "-u", creds.User}
		if creds.Password != "" {
			env = append(env, "MYSQL_PWD="+creds.Password)
		}
	case DatabaseMySQL:
		cmd = []string{"mysql", "-u", creds.User}
		if creds.Password != "" {
			env = append(env, "MYSQL_PWD="+creds.Password)
		}
	}
	return cmd, env
}

// databaseDumpEnabled reports whether a logical dump was requested for this
// item. Off unless asked for: a dump runs commands inside a live container and
// costs extra space, so it is opted into rather than assumed.
func databaseDumpEnabled(settings map[string]any) bool {
	v, ok := settings["database_dump"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// dumpDatabase writes a logical dump of the container's database into destDir,
// compressed with the job's codec, and returns the file for the backup result.
//
// The dump is written IN ADDITION to the volume archives, never instead of
// them: a dump that turns out to be misconfigured must not cost the file-level
// backup that would otherwise have been taken.
//
// The container must be running — a dump talks to the live server. Callers on
// the cold path skip this step rather than starting the container.
func (h *ContainerHandler) dumpDatabase(ctx context.Context, containerID, itemName string, image string, env []string, destDir, compression string, progress ProgressFunc) (*BackupFile, error) {
	kind := DetectDatabase(image, env)
	if kind == DatabaseNone {
		return nil, nil
	}
	creds := databaseCredentials(kind, env)
	cmd, cmdEnv := dumpCommand(kind, creds)
	if len(cmd) == 0 {
		return nil, nil
	}

	progress(itemName, 70, fmt.Sprintf("dumping %s database", kind))
	log.Printf("engine: %s: taking a %s logical dump as user %q", itemName, kind, creds.User)

	dumpPath := filepath.Join(destDir, DatabaseDumpFile+archiveExt(compression))
	out, err := os.Create(dumpPath) // #nosec G304 — destDir is a vault-controlled staging directory
	if err != nil {
		return nil, fmt.Errorf("creating database dump file: %w", err)
	}
	cw, closeCompress, err := compressedWriter(out, compression)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dumpPath)
		return nil, fmt.Errorf("compressing database dump: %w", err)
	}

	if err := h.execInContainer(ctx, containerID, cmd, cmdEnv, nil, cw); err != nil {
		_ = closeCompress()
		_ = out.Close()
		_ = os.Remove(dumpPath)
		return nil, fmt.Errorf("dumping %s database for %s: %w", kind, itemName, err)
	}
	if err := closeCompress(); err != nil {
		_ = out.Close()
		_ = os.Remove(dumpPath)
		return nil, fmt.Errorf("finalising database dump compression: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dumpPath)
		return nil, fmt.Errorf("closing database dump: %w", err)
	}

	info := backupFileInfo(dumpPath)
	// An empty dump means the command produced nothing — a server that was not
	// actually reachable, or credentials that let the tool start but see no
	// data. Reporting success here would hand back a restore that silently
	// recreates nothing.
	if info.Size == 0 {
		_ = os.Remove(dumpPath)
		return nil, fmt.Errorf("dumping %s database for %s: the dump was empty", kind, itemName)
	}
	log.Printf("engine: %s: %s dump written (%d bytes compressed)", itemName, kind, info.Size)
	return &info, nil
}

// restoreDatabase reloads a dump into a running container.
//
// Best-effort by design: the volume data has already been restored by the time
// this runs, so a failure here leaves the container in the state the file-level
// restore produced rather than losing anything. The error is returned so the
// caller can surface it as a warning.
func (h *ContainerHandler) restoreDatabase(ctx context.Context, containerID, itemName, image string, env []string, dumpPath string) error {
	kind := DetectDatabase(image, env)
	if kind == DatabaseNone {
		return nil
	}
	creds := databaseCredentials(kind, env)
	cmd, cmdEnv := restoreCommand(kind, creds)
	if len(cmd) == 0 {
		return nil
	}

	f, err := os.Open(dumpPath) // #nosec G304 — path derived from the restore staging directory
	if err != nil {
		return fmt.Errorf("opening database dump: %w", err)
	}
	defer f.Close()

	// Sniffed by magic bytes rather than derived from the filename, so a dump
	// restored from a backup taken under a different compression setting still
	// decodes — the same reason the archive path uses it.
	reader, closeReader, err := detectingReader(f)
	if err != nil {
		return fmt.Errorf("decompressing database dump: %w", err)
	}
	defer func() { _ = closeReader() }()

	log.Printf("engine: %s: reloading %s dump", itemName, kind)
	if err := h.execInContainer(ctx, containerID, cmd, cmdEnv, reader, io.Discard); err != nil {
		return fmt.Errorf("reloading %s dump for %s: %w", kind, itemName, err)
	}
	return nil
}

// dumpDatabaseToTemp streams a logical dump to an uncompressed temp file and
// returns its path plus a cleanup func.
//
// Used by the dedup/chunked path, which chunks the plain bytes: compressing
// first would defeat deduplication, since two dumps differing in one row
// produce entirely different compressed output.
func (h *ContainerHandler) dumpDatabaseToTemp(ctx context.Context, containerID, itemName, image string, env []string) (string, func(), error) {
	kind := DetectDatabase(image, env)
	if kind == DatabaseNone {
		return "", nil, nil
	}
	creds := databaseCredentials(kind, env)
	cmd, cmdEnv := dumpCommand(kind, creds)
	if len(cmd) == 0 {
		return "", nil, nil
	}

	f, err := os.CreateTemp("", "vault-dbdump-*.sql")
	if err != nil {
		return "", nil, fmt.Errorf("creating database dump temp file: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	log.Printf("engine: %s: taking a %s logical dump as user %q", itemName, kind, creds.User)
	if err := h.execInContainer(ctx, containerID, cmd, cmdEnv, nil, f); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("dumping %s database for %s: %w", kind, itemName, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("closing database dump: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("stat database dump: %w", err)
	}
	// An empty dump means the command produced nothing — a server that was not
	// reachable, or credentials that let the tool start but see no data.
	// Reporting success would hand back a restore that recreates nothing.
	if info.Size() == 0 {
		cleanup()
		return "", nil, fmt.Errorf("dumping %s database for %s: the dump was empty", kind, itemName)
	}
	log.Printf("engine: %s: %s dump written (%d bytes)", itemName, kind, info.Size())
	return path, cleanup, nil
}

// chunkFileIntoRepo splits a file into the dedup repo and returns the manifest
// entry describing it. Mirrors FolderHandler.BackupChunked's per-file loop so
// the dump is stored exactly like any other chunked content.
func chunkFileIntoRepo(repo *dedup.Repo, path string) (dedup.ManifestEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return dedup.ManifestEntry{}, err
	}
	f, err := os.Open(path) // #nosec G304 — path is a vault-created temp file
	if err != nil {
		return dedup.ManifestEntry{}, err
	}
	defer f.Close()

	chunker, err := dedup.NewChunker(repo.SplitterSecret())
	if err != nil {
		return dedup.ManifestEntry{}, err
	}
	ids := []dedup.ID{}
	if err := chunker.Split(f, func(chunk []byte) error {
		id, err := repo.Put(chunk)
		if err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return dedup.ManifestEntry{}, err
	}
	return dedup.ManifestEntry{Size: info.Size(), Chunks: ids}, nil
}

// databaseReadyProbe returns a cheap command that succeeds once the server is
// accepting connections.
func databaseReadyProbe(kind DatabaseKind, creds dbCredentials) (cmd []string, env []string) {
	switch kind {
	case DatabasePostgres:
		cmd = []string{"pg_isready", "-U", creds.User}
	case DatabaseMariaDB, DatabaseMySQL:
		cmd = []string{"sh", "-c",
			"if command -v mariadb-admin >/dev/null 2>&1; then exec mariadb-admin ping -u \"$1\"; else exec mysqladmin ping -u \"$1\"; fi",
			"sh", creds.User}
		if creds.Password != "" {
			env = append(env, "MYSQL_PWD="+creds.Password)
		}
	}
	if kind == DatabasePostgres && creds.Password != "" {
		env = append(env, "PGPASSWORD="+creds.Password)
	}
	return cmd, env
}

// waitForDatabaseReady blocks until the freshly-started server accepts
// connections, or the timeout expires.
//
// A restored container has to initialise its data directory and start the
// server before it can accept a dump, which takes several seconds. Reloading
// immediately would fail against a server that is merely still starting — a
// misleading error for a restore that is otherwise fine.
func (h *ContainerHandler) waitForDatabaseReady(ctx context.Context, containerID, image string, env []string) error {
	kind := DetectDatabase(image, env)
	if kind == DatabaseNone {
		return nil
	}
	cmd, cmdEnv := databaseReadyProbe(kind, databaseCredentials(kind, env))
	if len(cmd) == 0 {
		return nil
	}

	deadline := time.Now().Add(databaseReadyTimeout)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := h.execInContainer(ctx, containerID, cmd, cmdEnv, nil, io.Discard); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("database not ready after %s: %w", databaseReadyTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(databaseReadyPoll):
		}
	}
}
