# Vault Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Build a Veeam-style backup & restore plugin for Unraid 7+ that handles Docker containers and VMs with local and remote storage.

**Architecture:** Go daemon+CLI binary with REST API and WebSocket, PHP frontend for Unraid web UI, SQLite for state. Docker SDK and libvirt API for operations.

**Tech Stack:** Go 1.22+, SQLite (modernc.org/sqlite), Docker SDK, go-libvirt, chi router, gorilla/websocket, cobra CLI, aws-sdk-go-v2, go-smb2, pkg/sftp

---

### Task 0: Project Scaffolding

**Files:**

- Create: `go.mod`
- Create: `Makefile`
- Create: `cmd/vault/main.go`
- Create: `internal/config/types.go`

**Step 1: Initialize Go module**

```bash
cd /Users/ruaandeysel/Github/unraid-backups
go mod init github.com/ruaandeysel/vault
```

**Step 2: Create directory structure**

```bash
mkdir -p cmd/vault internal/{api/handlers,ws,cli,db,engine,storage,scheduler,notify,config} plugin/{pages/include,assets/{js,css}}
```

**Step 3: Create main.go entry point**

Create `cmd/vault/main.go`:

```go
package main

import (
 "os"

 "github.com/ruaandeysel/vault/internal/cli"
)

func main() {
 if err := cli.Execute(); err != nil {
  os.Exit(1)
 }
}
```

**Step 4: Create CLI root command**

Create `internal/cli/root.go`:

```go
package cli

import (
 "github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
 Use:   "vault",
 Short: "Vault - Unraid Backup & Restore",
}

func Execute() error {
 return rootCmd.Execute()
}
```

**Step 5: Create config types**

Create `internal/config/types.go`:

```go
package config

type CompressionType string

const (
 CompressionNone CompressionType = "none"
 CompressionGzip CompressionType = "gzip"
 CompressionZstd CompressionType = "zstd"
)

type BackupType string

const (
 BackupFull         BackupType = "full"
 BackupIncremental  BackupType = "incremental"
 BackupDifferential BackupType = "differential"
)

type VMBackupMode string

const (
 VMBackupLive VMBackupMode = "live"
 VMBackupCold VMBackupMode = "cold"
)

type ContainerBackupMode string

const (
 ContainerStopAll    ContainerBackupMode = "stop_all"
 ContainerOneByOne   ContainerBackupMode = "one_by_one"
)

type StorageType string

const (
 StorageLocal StorageType = "local"
 StorageSMB   StorageType = "smb"
 StorageNFS   StorageType = "nfs"
 StorageSFTP  StorageType = "sftp"
 StorageS3    StorageType = "s3"
)
```

**Step 6: Install dependencies and verify build**

```bash
go get github.com/spf13/cobra@latest
go mod tidy
go build ./cmd/vault/
```

Expected: Binary compiles with no errors.

**Step 7: Commit**

```bash
git add -A
git commit -m "feat: scaffold vault project with Go module, CLI, and config types"
```

---

### Task 1: SQLite Database Layer

**Files:**

- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`
- Create: `internal/db/models.go`
- Create: `internal/db/migrations.go`

**Step 1: Write failing test for database initialization**

Create `internal/db/db_test.go`:

```go
package db

import (
 "os"
 "path/filepath"
 "testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
 dir := t.TempDir()
 dbPath := filepath.Join(dir, "test.db")

 database, err := Open(dbPath)
 if err != nil {
  t.Fatalf("Open() error = %v", err)
 }
 defer database.Close()

 if _, err := os.Stat(dbPath); os.IsNotExist(err) {
  t.Fatal("database file was not created")
 }
}

func TestMigrationsCreateTables(t *testing.T) {
 dir := t.TempDir()
 database, err := Open(filepath.Join(dir, "test.db"))
 if err != nil {
  t.Fatalf("Open() error = %v", err)
 }
 defer database.Close()

 tables := []string{"jobs", "job_runs", "restore_points", "storage_destinations"}
 for _, table := range tables {
  var name string
  err := database.QueryRow(
   "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
  ).Scan(&name)
  if err != nil {
   t.Errorf("table %q not found: %v", table, err)
  }
 }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/db/ -v
```

Expected: FAIL — `Open` not defined.

**Step 3: Create models**

Create `internal/db/models.go`:

```go
package db

import "time"

type Job struct {
 ID                int64           `json:"id"`
 Name              string          `json:"name"`
 Description       string          `json:"description"`
 Enabled           bool            `json:"enabled"`
 Schedule          string          `json:"schedule"`
 BackupTypeChain   string          `json:"backup_type_chain"`
 RetentionCount    int             `json:"retention_count"`
 RetentionDays     int             `json:"retention_days"`
 Compression       string          `json:"compression"`
 ContainerMode     string          `json:"container_mode"`
 PreScript         string          `json:"pre_script"`
 PostScript        string          `json:"post_script"`
 NotifyOn          string          `json:"notify_on"`
 StorageDestID     int64           `json:"storage_dest_id"`
 CreatedAt         time.Time       `json:"created_at"`
 UpdatedAt         time.Time       `json:"updated_at"`
}

type JobItem struct {
 ID        int64  `json:"id"`
 JobID     int64  `json:"job_id"`
 ItemType  string `json:"item_type"` // "container" or "vm"
 ItemName  string `json:"item_name"`
 ItemID    string `json:"item_id"`
 Settings  string `json:"settings"` // JSON blob for per-item settings
}

type JobRun struct {
 ID          int64     `json:"id"`
 JobID       int64     `json:"job_id"`
 Status      string    `json:"status"` // running, success, partial, failed
 BackupType  string    `json:"backup_type"`
 StartedAt   time.Time `json:"started_at"`
 CompletedAt time.Time `json:"completed_at"`
 Log         string    `json:"log"`
 ItemsTotal  int       `json:"items_total"`
 ItemsDone   int       `json:"items_done"`
 ItemsFailed int       `json:"items_failed"`
 SizeBytes   int64     `json:"size_bytes"`
}

type RestorePoint struct {
 ID           int64     `json:"id"`
 JobRunID     int64     `json:"job_run_id"`
 JobID        int64     `json:"job_id"`
 BackupType   string    `json:"backup_type"`
 StoragePath  string    `json:"storage_path"`
 Metadata     string    `json:"metadata"` // JSON blob
 SizeBytes    int64     `json:"size_bytes"`
 CreatedAt    time.Time `json:"created_at"`
}

type StorageDestination struct {
 ID        int64     `json:"id"`
 Name      string    `json:"name"`
 Type      string    `json:"type"`
 Config    string    `json:"config"` // JSON blob with connection details
 CreatedAt time.Time `json:"created_at"`
 UpdatedAt time.Time `json:"updated_at"`
}
```

**Step 4: Create migrations**

Create `internal/db/migrations.go`:

```go
package db

const schema = `
CREATE TABLE IF NOT EXISTS storage_destinations (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL UNIQUE,
 type TEXT NOT NULL,
 config TEXT NOT NULL DEFAULT '{}',
 created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL UNIQUE,
 description TEXT DEFAULT '',
 enabled INTEGER DEFAULT 1,
 schedule TEXT DEFAULT '',
 backup_type_chain TEXT DEFAULT 'full',
 retention_count INTEGER DEFAULT 7,
 retention_days INTEGER DEFAULT 30,
 compression TEXT DEFAULT 'zstd',
 container_mode TEXT DEFAULT 'one_by_one',
 pre_script TEXT DEFAULT '',
 post_script TEXT DEFAULT '',
 notify_on TEXT DEFAULT 'failure',
 storage_dest_id INTEGER REFERENCES storage_destinations(id),
 created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS job_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 item_type TEXT NOT NULL,
 item_name TEXT NOT NULL,
 item_id TEXT NOT NULL,
 settings TEXT DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS job_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'running',
 backup_type TEXT NOT NULL,
 started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
 completed_at DATETIME,
 log TEXT DEFAULT '',
 items_total INTEGER DEFAULT 0,
 items_done INTEGER DEFAULT 0,
 items_failed INTEGER DEFAULT 0,
 size_bytes INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS restore_points (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 job_run_id INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
 job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 backup_type TEXT NOT NULL,
 storage_path TEXT NOT NULL,
 metadata TEXT DEFAULT '{}',
 size_bytes INTEGER DEFAULT 0,
 created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func migrate(db interface{ Exec(string, ...any) (interface{}, error) }) error {
 _, err := db.Exec(schema)
 return err
}
```

**Step 5: Create database Open/Close**

Create `internal/db/db.go`:

```go
package db

import (
 "database/sql"
 "fmt"

 _ "modernc.org/sqlite"
)

type DB struct {
 *sql.DB
}

func Open(path string) (*DB, error) {
 sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
 if err != nil {
  return nil, fmt.Errorf("open database: %w", err)
 }

 if err := sqlDB.Ping(); err != nil {
  sqlDB.Close()
  return nil, fmt.Errorf("ping database: %w", err)
 }

 if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
  sqlDB.Close()
  return nil, fmt.Errorf("enable foreign keys: %w", err)
 }

 if _, execErr := sqlDB.Exec(schema); execErr != nil {
  sqlDB.Close()
  return nil, fmt.Errorf("run migrations: %w", execErr)
 }

 return &DB{sqlDB}, nil
}
```

**Step 6: Install dependency, run tests**

```bash
go get modernc.org/sqlite@latest
go mod tidy
go test ./internal/db/ -v
```

Expected: PASS

**Step 7: Commit**

```bash
git add -A
git commit -m "feat: add SQLite database layer with schema and migrations"
```

---

### Task 2: Storage Adapter Interface + Local Adapter

**Files:**

- Create: `internal/storage/adapter.go`
- Create: `internal/storage/local.go`
- Create: `internal/storage/local_test.go`

**Step 1: Write failing test for local storage adapter**

Create `internal/storage/local_test.go`:

```go
package storage

import (
 "bytes"
 "io"
 "path/filepath"
 "testing"
)

func TestLocalWrite(t *testing.T) {
 dir := t.TempDir()
 adapter := NewLocalAdapter(dir)

 data := []byte("hello vault")
 err := adapter.Write("test/backup.tar", bytes.NewReader(data))
 if err != nil {
  t.Fatalf("Write() error = %v", err)
 }

 r, err := adapter.Read("test/backup.tar")
 if err != nil {
  t.Fatalf("Read() error = %v", err)
 }
 defer r.Close()

 got, _ := io.ReadAll(r)
 if !bytes.Equal(got, data) {
  t.Errorf("Read() = %q, want %q", got, data)
 }
}

func TestLocalList(t *testing.T) {
 dir := t.TempDir()
 adapter := NewLocalAdapter(dir)

 adapter.Write("backups/a.tar", bytes.NewReader([]byte("a")))
 adapter.Write("backups/b.tar", bytes.NewReader([]byte("b")))
 adapter.Write("other/c.tar", bytes.NewReader([]byte("c")))

 files, err := adapter.List("backups")
 if err != nil {
  t.Fatalf("List() error = %v", err)
 }
 if len(files) != 2 {
  t.Errorf("List() returned %d files, want 2", len(files))
 }
}

func TestLocalDelete(t *testing.T) {
 dir := t.TempDir()
 adapter := NewLocalAdapter(dir)

 adapter.Write("test.tar", bytes.NewReader([]byte("data")))
 if err := adapter.Delete("test.tar"); err != nil {
  t.Fatalf("Delete() error = %v", err)
 }

 _, err := adapter.Read("test.tar")
 if err == nil {
  t.Error("Read() after Delete() should fail")
 }
}

func TestLocalTestConnection(t *testing.T) {
 dir := t.TempDir()
 adapter := NewLocalAdapter(dir)
 if err := adapter.TestConnection(); err != nil {
  t.Fatalf("TestConnection() error = %v", err)
 }

 bad := NewLocalAdapter(filepath.Join(dir, "nonexistent"))
 if err := bad.TestConnection(); err == nil {
  t.Error("TestConnection() on bad path should fail")
 }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/storage/ -v
```

Expected: FAIL

**Step 3: Create adapter interface**

Create `internal/storage/adapter.go`:

```go
package storage

import (
 "io"
 "time"
)

type FileInfo struct {
 Path    string    `json:"path"`
 Size    int64     `json:"size"`
 ModTime time.Time `json:"mod_time"`
 IsDir   bool      `json:"is_dir"`
}

type Adapter interface {
 Write(path string, reader io.Reader) error
 Read(path string) (io.ReadCloser, error)
 Delete(path string) error
 List(prefix string) ([]FileInfo, error)
 Stat(path string) (FileInfo, error)
 TestConnection() error
}
```

**Step 4: Implement local adapter**

Create `internal/storage/local.go`:

```go
package storage

import (
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
)

type LocalAdapter struct {
 basePath string
}

func NewLocalAdapter(basePath string) *LocalAdapter {
 return &LocalAdapter{basePath: basePath}
}

func (l *LocalAdapter) fullPath(path string) string {
 return filepath.Join(l.basePath, filepath.Clean(path))
}

func (l *LocalAdapter) Write(path string, reader io.Reader) error {
 full := l.fullPath(path)
 if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
  return fmt.Errorf("create directories: %w", err)
 }
 f, err := os.Create(full)
 if err != nil {
  return fmt.Errorf("create file: %w", err)
 }
 defer f.Close()
 if _, err := io.Copy(f, reader); err != nil {
  return fmt.Errorf("write file: %w", err)
 }
 return nil
}

func (l *LocalAdapter) Read(path string) (io.ReadCloser, error) {
 return os.Open(l.fullPath(path))
}

func (l *LocalAdapter) Delete(path string) error {
 return os.Remove(l.fullPath(path))
}

func (l *LocalAdapter) List(prefix string) ([]FileInfo, error) {
 dir := l.fullPath(prefix)
 entries, err := os.ReadDir(dir)
 if err != nil {
  return nil, err
 }
 var files []FileInfo
 for _, e := range entries {
  info, err := e.Info()
  if err != nil {
   continue
  }
  files = append(files, FileInfo{
   Path:    filepath.Join(prefix, e.Name()),
   Size:    info.Size(),
   ModTime: info.ModTime(),
   IsDir:   e.IsDir(),
  })
 }
 return files, nil
}

func (l *LocalAdapter) Stat(path string) (FileInfo, error) {
 info, err := os.Stat(l.fullPath(path))
 if err != nil {
  return FileInfo{}, err
 }
 return FileInfo{
  Path:    path,
  Size:    info.Size(),
  ModTime: info.ModTime(),
  IsDir:   info.IsDir(),
 }, nil
}

func (l *LocalAdapter) TestConnection() error {
 info, err := os.Stat(l.basePath)
 if err != nil {
  return fmt.Errorf("path not accessible: %w", err)
 }
 if !info.IsDir() {
  return fmt.Errorf("%s is not a directory", l.basePath)
 }
 // Test write permission
 testFile := filepath.Join(l.basePath, ".vault_test")
 if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
  return fmt.Errorf("not writable: %w", err)
 }
 os.Remove(testFile)
 return nil
}

// Verify interface compliance
var _ Adapter = (*LocalAdapter)(nil)

// Sanitize ensures path doesn't escape basePath
func sanitizePath(path string) string {
 cleaned := filepath.Clean(path)
 cleaned = strings.TrimPrefix(cleaned, "/")
 cleaned = strings.TrimPrefix(cleaned, "../")
 return cleaned
}
```

**Step 5: Run tests**

```bash
go test ./internal/storage/ -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add -A
git commit -m "feat: add storage adapter interface and local adapter"
```

---

### Task 3: REST API Server Skeleton

**Files:**

- Create: `internal/api/server.go`
- Create: `internal/api/server_test.go`
- Create: `internal/api/routes.go`
- Modify: `internal/cli/root.go`
- Create: `internal/cli/daemon.go`

**Step 1: Write failing test for API server health endpoint**

Create `internal/api/server_test.go`:

```go
package api

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "path/filepath"
 "testing"

 "github.com/ruaandeysel/vault/internal/db"
)

func testDB(t *testing.T) *db.DB {
 t.Helper()
 d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
 if err != nil {
  t.Fatal(err)
 }
 t.Cleanup(func() { d.Close() })
 return d
}

func TestHealthEndpoint(t *testing.T) {
 database := testDB(t)
 srv := NewServer(database, ":0")

 req := httptest.NewRequest("GET", "/api/v1/health", nil)
 w := httptest.NewRecorder()
 srv.router.ServeHTTP(w, req)

 if w.Code != http.StatusOK {
  t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
 }

 var resp map[string]string
 json.NewDecoder(w.Body).Decode(&resp)
 if resp["status"] != "ok" {
  t.Errorf("status = %q, want %q", resp["status"], "ok")
 }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/api/ -v
```

Expected: FAIL

**Step 3: Create API server and routes**

Create `internal/api/server.go`:

```go
package api

import (
 "context"
 "encoding/json"
 "log"
 "net/http"
 "time"

 "github.com/go-chi/chi/v5"
 "github.com/go-chi/chi/v5/middleware"
 "github.com/ruaandeysel/vault/internal/db"
)

type Server struct {
 db     *db.DB
 router *chi.Mux
 addr   string
}

func NewServer(database *db.DB, addr string) *Server {
 s := &Server{
  db:   database,
  addr: addr,
 }
 s.router = s.setupRoutes()
 return s
}

func (s *Server) Start() error {
 srv := &http.Server{
  Addr:         s.addr,
  Handler:      s.router,
  ReadTimeout:  15 * time.Second,
  WriteTimeout: 15 * time.Second,
 }
 log.Printf("Vault API server listening on %s", s.addr)
 return srv.ListenAndServe()
}

func (s *Server) StartWithContext(ctx context.Context) error {
 srv := &http.Server{
  Addr:         s.addr,
  Handler:      s.router,
  ReadTimeout:  15 * time.Second,
  WriteTimeout: 15 * time.Second,
 }

 go func() {
  <-ctx.Done()
  shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer cancel()
  srv.Shutdown(shutdownCtx)
 }()

 log.Printf("Vault API server listening on %s", s.addr)
 return srv.ListenAndServe()
}

func respondJSON(w http.ResponseWriter, status int, data any) {
 w.Header().Set("Content-Type", "application/json")
 w.WriteHeader(status)
 json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, msg string) {
 respondJSON(w, status, map[string]string{"error": msg})
}
```

Create `internal/api/routes.go`:

```go
package api

import (
 "net/http"

 "github.com/go-chi/chi/v5"
 "github.com/go-chi/chi/v5/middleware"
)

func (s *Server) setupRoutes() *chi.Mux {
 r := chi.NewRouter()
 r.Use(middleware.Logger)
 r.Use(middleware.Recoverer)
 r.Use(middleware.Heartbeat("/ping"))

 r.Route("/api/v1", func(r chi.Router) {
  r.Get("/health", s.handleHealth)
 })

 return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
 respondJSON(w, http.StatusOK, map[string]string{
  "status":  "ok",
  "version": "0.1.0",
 })
}
```

**Step 4: Install chi, run tests**

```bash
go get github.com/go-chi/chi/v5@latest
go mod tidy
go test ./internal/api/ -v
```

Expected: PASS

**Step 5: Create daemon CLI command**

Create `internal/cli/daemon.go`:

```go
package cli

import (
 "log"

 "github.com/ruaandeysel/vault/internal/api"
 "github.com/ruaandeysel/vault/internal/db"
 "github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
 Use:   "daemon",
 Short: "Start the Vault daemon (API server + scheduler)",
 RunE: func(cmd *cobra.Command, args []string) error {
  dbPath, _ := cmd.Flags().GetString("db")
  addr, _ := cmd.Flags().GetString("addr")

  database, err := db.Open(dbPath)
  if err != nil {
   return err
  }
  defer database.Close()

  log.Println("Starting Vault daemon...")
  srv := api.NewServer(database, addr)
  return srv.Start()
 },
}

func init() {
 daemonCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Database path")
 daemonCmd.Flags().String("addr", ":28085", "API listen address")
 rootCmd.AddCommand(daemonCmd)
}
```

**Step 6: Verify build**

```bash
go build ./cmd/vault/
./vault --help
./vault daemon --help
```

Expected: Help output shows daemon subcommand.

**Step 7: Commit**

```bash
git add -A
git commit -m "feat: add REST API server with chi router and daemon CLI command"
```

---

### Task 4: WebSocket Server for Progress

**Files:**

- Create: `internal/ws/hub.go`
- Create: `internal/ws/hub_test.go`
- Modify: `internal/api/routes.go`

**Step 1: Write failing test for WebSocket hub**

Create `internal/ws/hub_test.go`:

```go
package ws

import (
 "testing"
 "time"
)

func TestHubBroadcast(t *testing.T) {
 hub := NewHub()
 go hub.Run()

 ch := make(chan []byte, 10)
 client := &Client{hub: hub, send: ch}
 hub.Register(client)

 time.Sleep(10 * time.Millisecond)

 hub.Broadcast([]byte(`{"type":"progress","percent":50}`))
 time.Sleep(10 * time.Millisecond)

 select {
 case msg := <-ch:
  if string(msg) != `{"type":"progress","percent":50}` {
   t.Errorf("got %q", string(msg))
  }
 default:
  t.Error("no message received")
 }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/ws/ -v
```

**Step 3: Implement WebSocket hub**

Create `internal/ws/hub.go`:

```go
package ws

import (
 "log"
 "net/http"
 "sync"

 "github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
 CheckOrigin: func(r *http.Request) bool { return true },
}

type Client struct {
 hub  *Hub
 conn *websocket.Conn
 send chan []byte
}

type Hub struct {
 clients    map[*Client]bool
 broadcast  chan []byte
 register   chan *Client
 unregister chan *Client
 mu         sync.RWMutex
}

func NewHub() *Hub {
 return &Hub{
  clients:    make(map[*Client]bool),
  broadcast:  make(chan []byte, 256),
  register:   make(chan *Client),
  unregister: make(chan *Client),
 }
}

func (h *Hub) Run() {
 for {
  select {
  case client := <-h.register:
   h.mu.Lock()
   h.clients[client] = true
   h.mu.Unlock()
  case client := <-h.unregister:
   h.mu.Lock()
   if _, ok := h.clients[client]; ok {
    delete(h.clients, client)
    close(client.send)
   }
   h.mu.Unlock()
  case msg := <-h.broadcast:
   h.mu.RLock()
   for client := range h.clients {
    select {
    case client.send <- msg:
    default:
     close(client.send)
     delete(h.clients, client)
    }
   }
   h.mu.RUnlock()
  }
 }
}

func (h *Hub) Register(c *Client) {
 h.register <- c
}

func (h *Hub) Unregister(c *Client) {
 h.unregister <- c
}

func (h *Hub) Broadcast(msg []byte) {
 h.broadcast <- msg
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
 conn, err := upgrader.Upgrade(w, r, nil)
 if err != nil {
  log.Printf("ws upgrade error: %v", err)
  return
 }
 client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
 h.Register(client)

 go client.writePump()
 go client.readPump()
}

func (c *Client) writePump() {
 defer func() {
  if c.conn != nil {
   c.conn.Close()
  }
 }()
 for msg := range c.send {
  if c.conn == nil {
   return
  }
  if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
   return
  }
 }
}

func (c *Client) readPump() {
 defer func() {
  c.hub.Unregister(c)
  if c.conn != nil {
   c.conn.Close()
  }
 }()
 for {
  if c.conn == nil {
   return
  }
  if _, _, err := c.conn.ReadMessage(); err != nil {
   return
  }
 }
}
```

**Step 4: Install gorilla/websocket, run tests**

```bash
go get github.com/gorilla/websocket@latest
go mod tidy
go test ./internal/ws/ -v
```

Expected: PASS

**Step 5: Wire WebSocket into API routes**

Add to `internal/api/server.go` — add `hub` field to Server struct.
Add to `internal/api/routes.go` — add `/api/v1/ws` route.

**Step 6: Commit**

```bash
git add -A
git commit -m "feat: add WebSocket hub for real-time progress updates"
```

---

### Task 5: Storage Destination CRUD API

**Files:**

- Create: `internal/db/storage_repo.go`
- Create: `internal/db/storage_repo_test.go`
- Create: `internal/api/handlers/storage.go`
- Create: `internal/api/handlers/storage_test.go`
- Modify: `internal/api/routes.go`

**Step 1: Write failing test for storage repository**

Create `internal/db/storage_repo_test.go`:

```go
package db

import (
 "path/filepath"
 "testing"
)

func setupTestDB(t *testing.T) *DB {
 t.Helper()
 d, err := Open(filepath.Join(t.TempDir(), "test.db"))
 if err != nil {
  t.Fatal(err)
 }
 t.Cleanup(func() { d.Close() })
 return d
}

func TestCreateAndGetStorageDestination(t *testing.T) {
 d := setupTestDB(t)

 dest := StorageDestination{
  Name:   "local-backup",
  Type:   "local",
  Config: `{"path":"/mnt/user/backups"}`,
 }

 id, err := d.CreateStorageDestination(dest)
 if err != nil {
  t.Fatalf("Create error = %v", err)
 }

 got, err := d.GetStorageDestination(id)
 if err != nil {
  t.Fatalf("Get error = %v", err)
 }
 if got.Name != "local-backup" {
  t.Errorf("Name = %q, want %q", got.Name, "local-backup")
 }
}

func TestListStorageDestinations(t *testing.T) {
 d := setupTestDB(t)

 d.CreateStorageDestination(StorageDestination{Name: "a", Type: "local", Config: "{}"})
 d.CreateStorageDestination(StorageDestination{Name: "b", Type: "smb", Config: "{}"})

 dests, err := d.ListStorageDestinations()
 if err != nil {
  t.Fatalf("List error = %v", err)
 }
 if len(dests) != 2 {
  t.Errorf("got %d destinations, want 2", len(dests))
 }
}

func TestDeleteStorageDestination(t *testing.T) {
 d := setupTestDB(t)

 id, _ := d.CreateStorageDestination(StorageDestination{Name: "del", Type: "local", Config: "{}"})
 if err := d.DeleteStorageDestination(id); err != nil {
  t.Fatalf("Delete error = %v", err)
 }

 _, err := d.GetStorageDestination(id)
 if err == nil {
  t.Error("Get after Delete should fail")
 }
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/db/ -v -run Storage
```

**Step 3: Implement storage repository**

Create `internal/db/storage_repo.go`:

```go
package db

import "database/sql"

func (d *DB) CreateStorageDestination(dest StorageDestination) (int64, error) {
 res, err := d.Exec(
  "INSERT INTO storage_destinations (name, type, config) VALUES (?, ?, ?)",
  dest.Name, dest.Type, dest.Config,
 )
 if err != nil {
  return 0, err
 }
 return res.LastInsertId()
}

func (d *DB) GetStorageDestination(id int64) (StorageDestination, error) {
 var dest StorageDestination
 err := d.QueryRow(
  "SELECT id, name, type, config, created_at, updated_at FROM storage_destinations WHERE id = ?", id,
 ).Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.CreatedAt, &dest.UpdatedAt)
 if err == sql.ErrNoRows {
  return dest, ErrNotFound
 }
 return dest, err
}

func (d *DB) ListStorageDestinations() ([]StorageDestination, error) {
 rows, err := d.Query("SELECT id, name, type, config, created_at, updated_at FROM storage_destinations ORDER BY name")
 if err != nil {
  return nil, err
 }
 defer rows.Close()

 var dests []StorageDestination
 for rows.Next() {
  var dest StorageDestination
  if err := rows.Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.CreatedAt, &dest.UpdatedAt); err != nil {
   return nil, err
  }
  dests = append(dests, dest)
 }
 return dests, rows.Err()
}

func (d *DB) UpdateStorageDestination(dest StorageDestination) error {
 _, err := d.Exec(
  "UPDATE storage_destinations SET name=?, type=?, config=?, updated_at=CURRENT_TIMESTAMP WHERE id=?",
  dest.Name, dest.Type, dest.Config, dest.ID,
 )
 return err
}

func (d *DB) DeleteStorageDestination(id int64) error {
 _, err := d.Exec("DELETE FROM storage_destinations WHERE id = ?", id)
 return err
}
```

Add to `internal/db/db.go`:

```go
import "errors"

var ErrNotFound = errors.New("not found")
```

**Step 4: Run tests**

```bash
go test ./internal/db/ -v
```

Expected: PASS

**Step 5: Create API handlers for storage destinations**

Create `internal/api/handlers/storage.go` with standard CRUD handlers that call the DB repository and respond with JSON.

**Step 6: Wire routes**

Add to `internal/api/routes.go`:

```go
r.Route("/storage", func(r chi.Router) {
    r.Get("/", s.handleListStorage)
    r.Post("/", s.handleCreateStorage)
    r.Get("/{id}", s.handleGetStorage)
    r.Put("/{id}", s.handleUpdateStorage)
    r.Delete("/{id}", s.handleDeleteStorage)
    r.Post("/{id}/test", s.handleTestStorage)
})
```

**Step 7: Commit**

```bash
git add -A
git commit -m "feat: add storage destination CRUD with API endpoints"
```

---

### Task 6: Job CRUD API

**Files:**

- Create: `internal/db/job_repo.go`
- Create: `internal/db/job_repo_test.go`
- Modify: `internal/api/routes.go`

Same pattern as Task 5 but for Jobs and JobItems. CRUD operations for creating, listing, updating, deleting backup jobs with their associated items.

**Routes:**

```
GET    /api/v1/jobs          - List jobs
POST   /api/v1/jobs          - Create job
GET    /api/v1/jobs/{id}     - Get job details + items
PUT    /api/v1/jobs/{id}     - Update job
DELETE /api/v1/jobs/{id}     - Delete job
POST   /api/v1/jobs/{id}/run - Trigger manual run
```

**Commit message:** `feat: add backup job CRUD with API endpoints`

---

### Task 7: Container Backup Engine (Docker SDK)

**Files:**

- Create: `internal/engine/container.go`
- Create: `internal/engine/container_test.go`
- Create: `internal/engine/types.go`

**Step 1: Create engine types**

Create `internal/engine/types.go`:

```go
package engine

import "io"

type BackupItem struct {
 Name     string
 Type     string // "container" or "vm"
 Settings map[string]any
}

type BackupResult struct {
 ItemName string
 Success  bool
 Error    string
 Files    []BackupFile
}

type BackupFile struct {
 Name string
 Size int64
}

type ProgressFunc func(item string, percent int, message string)

type Handler interface {
 Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
 Restore(item BackupItem, source string, progress ProgressFunc) error
 ListItems() ([]BackupItem, error)
}
```

**Step 2: Implement container handler**

Create `internal/engine/container.go`:

```go
package engine

import (
 "archive/tar"
 "compress/gzip"
 "context"
 "encoding/json"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "time"

 "github.com/docker/docker/api/types"
 "github.com/docker/docker/api/types/container"
 "github.com/docker/docker/client"
)

type ContainerHandler struct {
 cli *client.Client
}

func NewContainerHandler() (*ContainerHandler, error) {
 cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
 if err != nil {
  return nil, fmt.Errorf("create docker client: %w", err)
 }
 return &ContainerHandler{cli: cli}, nil
}

func (h *ContainerHandler) ListItems() ([]BackupItem, error) {
 ctx := context.Background()
 containers, err := h.cli.ContainerList(ctx, container.ListOptions{All: true})
 if err != nil {
  return nil, err
 }

 var items []BackupItem
 for _, c := range containers {
  name := ""
  if len(c.Names) > 0 {
   name = c.Names[0][1:] // strip leading /
  }
  items = append(items, BackupItem{
   Name: name,
   Type: "container",
   Settings: map[string]any{
    "id":    c.ID,
    "image": c.Image,
    "state": c.State,
   },
  })
 }
 return items, nil
}

func (h *ContainerHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
 ctx := context.Background()
 containerID := item.Settings["id"].(string)
 result := &BackupResult{ItemName: item.Name}

 progress(item.Name, 0, "Inspecting container")
 inspect, err := h.cli.ContainerInspect(ctx, containerID)
 if err != nil {
  result.Error = err.Error()
  return result, err
 }

 // Save container config
 progress(item.Name, 10, "Saving configuration")
 configData, _ := json.MarshalIndent(inspect, "", "  ")
 configPath := filepath.Join(destDir, "config.json")
 if err := os.WriteFile(configPath, configData, 0644); err != nil {
  result.Error = err.Error()
  return result, err
 }

 // Stop container if running
 wasRunning := inspect.State.Running
 noStop, _ := item.Settings["no_stop"].(bool)
 if wasRunning && !noStop {
  progress(item.Name, 20, "Stopping container")
  timeout := 30
  stopOpts := container.StopOptions{Timeout: &timeout}
  if err := h.cli.ContainerStop(ctx, containerID, stopOpts); err != nil {
   result.Error = fmt.Sprintf("stop container: %v", err)
   return result, err
  }
 }

 // Save image
 progress(item.Name, 30, "Saving image")
 imageReader, err := h.cli.ImageSave(ctx, []string{inspect.Image})
 if err == nil {
  imagePath := filepath.Join(destDir, "image.tar")
  f, _ := os.Create(imagePath)
  written, _ := io.Copy(f, imageReader)
  f.Close()
  imageReader.Close()
  result.Files = append(result.Files, BackupFile{Name: "image.tar", Size: written})
 }

 // Backup bind mounts
 progress(item.Name, 50, "Backing up volumes")
 for i, mount := range inspect.Mounts {
  if mount.Type != "bind" {
   continue
  }
  pct := 50 + (40 * (i + 1) / max(len(inspect.Mounts), 1))
  progress(item.Name, pct, fmt.Sprintf("Backing up %s", mount.Source))

  tarName := fmt.Sprintf("volume_%d.tar.gz", i)
  tarPath := filepath.Join(destDir, tarName)
  if err := tarDirectory(mount.Source, tarPath); err != nil {
   continue // log but don't fail entire backup
  }
  info, _ := os.Stat(tarPath)
  if info != nil {
   result.Files = append(result.Files, BackupFile{Name: tarName, Size: info.Size()})
  }
 }

 // Restart container if it was running
 if wasRunning && !noStop {
  progress(item.Name, 95, "Starting container")
  h.cli.ContainerStart(ctx, containerID, container.StartOptions{})
 }

 progress(item.Name, 100, "Complete")
 result.Success = true
 return result, nil
}

func (h *ContainerHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
 ctx := context.Background()

 // Load image
 progress(item.Name, 10, "Loading image")
 imagePath := filepath.Join(sourceDir, "image.tar")
 if f, err := os.Open(imagePath); err == nil {
  defer f.Close()
  h.cli.ImageLoad(ctx, f, true)
 }

 // Read config
 progress(item.Name, 30, "Reading configuration")
 configData, err := os.ReadFile(filepath.Join(sourceDir, "config.json"))
 if err != nil {
  return fmt.Errorf("read config: %w", err)
 }

 var inspect types.ContainerJSON
 if err := json.Unmarshal(configData, &inspect); err != nil {
  return fmt.Errorf("parse config: %w", err)
 }

 // Restore volumes
 progress(item.Name, 50, "Restoring volumes")
 for i, mount := range inspect.Mounts {
  if mount.Type != "bind" {
   continue
  }
  tarPath := filepath.Join(sourceDir, fmt.Sprintf("volume_%d.tar.gz", i))
  if _, err := os.Stat(tarPath); err != nil {
   continue
  }
  untarDirectory(tarPath, mount.Source)
 }

 progress(item.Name, 100, "Complete")
 return nil
}

func tarDirectory(src, dst string) error {
 f, err := os.Create(dst)
 if err != nil {
  return err
 }
 defer f.Close()

 gw := gzip.NewWriter(f)
 defer gw.Close()

 tw := tar.NewWriter(gw)
 defer tw.Close()

 return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
  if err != nil {
   return nil // skip errors
  }
  rel, _ := filepath.Rel(src, path)
  header, err := tar.FileInfoHeader(info, "")
  if err != nil {
   return nil
  }
  header.Name = rel
  tw.WriteHeader(header)
  if !info.IsDir() {
   f, err := os.Open(path)
   if err != nil {
    return nil
   }
   defer f.Close()
   io.Copy(tw, f)
  }
  return nil
 })
}

func untarDirectory(src, dst string) error {
 f, err := os.Open(src)
 if err != nil {
  return err
 }
 defer f.Close()

 gr, err := gzip.NewReader(f)
 if err != nil {
  return err
 }
 defer gr.Close()

 tr := tar.NewReader(gr)
 for {
  header, err := tr.Next()
  if err == io.EOF {
   break
  }
  if err != nil {
   return err
  }

  target := filepath.Join(dst, header.Name)
  switch header.Typeflag {
  case tar.TypeDir:
   os.MkdirAll(target, 0755)
  case tar.TypeReg:
   os.MkdirAll(filepath.Dir(target), 0755)
   f, _ := os.Create(target)
   io.Copy(f, tr)
   f.Close()
  }
 }
 return nil
}
```

**Step 3: Install Docker SDK, run tests**

```bash
go get github.com/docker/docker@latest
go mod tidy
go test ./internal/engine/ -v
```

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add container backup handler using Docker SDK"
```

---

### Task 8: VM Backup Engine (libvirt)

**Files:**

- Create: `internal/engine/vm.go`
- Create: `internal/engine/vm_test.go`

Same pattern as Task 7 but using go-libvirt for VM operations. Implements `Handler` interface with:

- `ListItems()` — enumerate VMs via libvirt
- `Backup()` — live snapshot or cold backup of vdisks, XML, NVRAM
- `Restore()` — define VM from XML, write vdisks

Key libvirt operations:

- `ConnectListAllDomains` for enumeration
- `DomainGetXMLDesc` for config extraction
- `DomainSnapshotCreateXML` for live snapshots
- `DomainShutdown` / `DomainCreate` for cold backups

**Commit message:** `feat: add VM backup handler using libvirt API`

---

### Task 9: Remote Storage Adapters (SFTP + S3)

**Files:**

- Create: `internal/storage/sftp.go`
- Create: `internal/storage/sftp_test.go`
- Create: `internal/storage/s3.go`
- Create: `internal/storage/s3_test.go`
- Create: `internal/storage/smb.go`
- Create: `internal/storage/factory.go`

Each adapter implements the `Adapter` interface. Factory function creates the right adapter from `StorageDestination.Type` and `Config` JSON.

Create `internal/storage/factory.go`:

```go
package storage

import (
 "encoding/json"
 "fmt"
)

func NewAdapter(storageType, configJSON string) (Adapter, error) {
 switch storageType {
 case "local":
  var cfg struct{ Path string }
  json.Unmarshal([]byte(configJSON), &cfg)
  return NewLocalAdapter(cfg.Path), nil
 case "sftp":
  var cfg SFTPConfig
  json.Unmarshal([]byte(configJSON), &cfg)
  return NewSFTPAdapter(cfg)
 case "s3":
  var cfg S3Config
  json.Unmarshal([]byte(configJSON), &cfg)
  return NewS3Adapter(cfg)
 case "smb":
  var cfg SMBConfig
  json.Unmarshal([]byte(configJSON), &cfg)
  return NewSMBAdapter(cfg)
 default:
  return nil, fmt.Errorf("unknown storage type: %s", storageType)
 }
}
```

**Commit message:** `feat: add SFTP, S3, and SMB storage adapters`

---

### Task 10: Job Scheduler

**Files:**

- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`

Uses `robfig/cron/v3` for cron-based scheduling. Loads enabled jobs from DB, creates cron entries, executes job runs.

```go
package scheduler

import (
 "github.com/robfig/cron/v3"
 "github.com/ruaandeysel/vault/internal/db"
)

type Scheduler struct {
 cron *cron.Cron
 db   *db.DB
}

func New(database *db.DB) *Scheduler {
 return &Scheduler{
  cron: cron.New(),
  db:   database,
 }
}

func (s *Scheduler) Start() error {
 // Load all enabled jobs, add cron entries
 s.cron.Start()
 return nil
}

func (s *Scheduler) Stop() {
 s.cron.Stop()
}

func (s *Scheduler) Reload() error {
 // Remove all entries and reload from DB
 return nil
}
```

**Commit message:** `feat: add cron-based job scheduler`

---

### Task 11: Job Execution Engine

**Files:**

- Create: `internal/engine/executor.go`
- Create: `internal/engine/executor_test.go`

Orchestrates a full job run: determines backup type from chain rules, runs pre/post scripts, iterates items, writes to storage, applies retention, sends notifications, records history.

**Commit message:** `feat: add job execution engine with retention and notifications`

---

### Task 12: Unraid Notification Integration

**Files:**

- Create: `internal/notify/unraid.go`
- Create: `internal/notify/unraid_test.go`

Unraid notifications via `/usr/local/emhttp/webGui/scripts/notify`:

```go
func Notify(event, subject, description, importance string) error {
 cmd := exec.Command("/usr/local/emhttp/webGui/scripts/notify",
  "-e", event,
  "-s", subject,
  "-d", description,
  "-i", importance,
 )
 return cmd.Run()
}
```

**Commit message:** `feat: add Unraid notification integration`

---

### Task 13: PHP Web UI — API Helper + Dashboard

**Files:**

- Create: `plugin/pages/include/api.php`
- Create: `plugin/pages/Vault.page`
- Create: `plugin/assets/css/vault.css`
- Create: `plugin/assets/js/vault.js`

PHP helper to call Go REST API:

```php
<?php
function vault_api($method, $endpoint, $data = null) {
    $url = "http://127.0.0.1:28085/api/v1" . $endpoint;
    $ch = curl_init($url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_CUSTOMREQUEST, $method);
    if ($data) {
        curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
        curl_setopt($ch, CURLOPT_HTTPHEADER, ['Content-Type: application/json']);
    }
    $response = curl_exec($ch);
    curl_close($ch);
    return json_decode($response, true);
}
?>
```

**Commit message:** `feat: add PHP web UI with dashboard page`

---

### Task 14: PHP Web UI — Jobs, Restore, Storage, History, Settings Pages

**Files:**

- Create: `plugin/pages/Vault.Jobs.page`
- Create: `plugin/pages/Vault.Restore.page`
- Create: `plugin/pages/Vault.Storage.page`
- Create: `plugin/pages/Vault.History.page`
- Create: `plugin/pages/Vault.Settings.page`
- Create: `plugin/assets/js/jobs.js`
- Create: `plugin/assets/js/restore.js`

Each page follows Unraid `.page` format with header + PHP/HTML content calling the REST API via the helper.

**Commit message:** `feat: add Jobs, Restore, Storage, History, and Settings UI pages`

---

### Task 15: Plugin Packaging

**Files:**

- Create: `plugin/vault.plg`
- Create: `plugin/rc.vault`
- Create: `Makefile` (update with build + package targets)

PLG file:

```xml
<?xml version='1.0' standalone='yes'?>
<!DOCTYPE PLUGIN [
    <!ENTITY name      "vault">
    <!ENTITY author    "ruaandeysel">
    <!ENTITY version   "0.1.0">
    <!ENTITY launch    "Settings/Vault">
    <!ENTITY pluginURL "https://raw.githubusercontent.com/ruaandeysel/vault/main/plugin/vault.plg">
]>
<PLUGIN name="&name;" author="&author;" version="&version;"
        launch="&launch;" pluginURL="&pluginURL;" icon="shield"
        min="7.0" support="">
    <!-- Install Go binary -->
    <FILE Name="/usr/local/sbin/vault" Mode="0755">
        <URL>https://github.com/ruaandeysel/vault/releases/download/v&version;/vault-linux-amd64</URL>
    </FILE>
    <!-- RC script -->
    <FILE Name="/etc/rc.d/rc.vault" Mode="0755">
        <INLINE>
        <![CDATA[
#!/bin/bash
case "$1" in
    start)  /usr/local/sbin/vault daemon &;;
    stop)   killall vault 2>/dev/null;;
    restart) $0 stop; sleep 1; $0 start;;
esac
        ]]>
        </INLINE>
    </FILE>
    <!-- Start daemon on install -->
    <FILE Run="/bin/bash">
        <INLINE>
        <![CDATA[
/etc/rc.d/rc.vault start
        ]]>
        </INLINE>
    </FILE>
    <!-- Removal -->
    <FILE Run="/bin/bash" Method="remove">
        <INLINE>
        <![CDATA[
/etc/rc.d/rc.vault stop
rm -f /usr/local/sbin/vault
rm -f /etc/rc.d/rc.vault
rm -rf /usr/local/emhttp/plugins/vault
        ]]>
        </INLINE>
    </FILE>
</PLUGIN>
```

RC script for daemon management. Makefile targets: `build`, `test`, `package`.

**Commit message:** `feat: add plugin packaging with .plg installer and RC script`

---

### Task 16: CI Pipeline + Release

**Files:**

- Create: `.github/workflows/build.yml`
- Create: `.github/workflows/release.yml`

Build workflow: lint, test, build linux/amd64 binary on every push.
Release workflow: on tag push, build binary and create GitHub release with the binary attached.

**Commit message:** `ci: add build and release workflows`
