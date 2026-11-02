package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tinkerologie/vault/internal/db"
)

// A health message quotes the status Docker reports. This one holds credentials,
// standing in for any text a message can carry.
const leakyContainerStatus = "http://admin:hunter2@127.0.0.1:8080/health?token=abc"

// fakeContainerDocker points the engine's Docker client at a loopback server
// knowing one container. Stopped, its status is leakyContainerStatus, and it stays
// stopped when started, so the health check that follows a backup reports it.
func fakeContainerDocker(t *testing.T, id string, running bool) {
	t.Helper()
	var isRunning atomic.Bool
	isRunning.Store(running)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": id, "Names": []string{"/" + id}, "State": "exited"}})
		case strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":         id,
				"Name":       "/" + id,
				"Config":     map[string]any{"Image": "fake:latest"},
				"HostConfig": map[string]any{},
				"State":      map[string]any{"Running": isRunning.Load(), "Status": leakyContainerStatus, "ExitCode": 3},
			})
		case strings.HasSuffix(r.URL.Path, "/containers/"+id+"/stop"):
			isRunning.Store(false)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/images/get"):
			w.Header().Set("Content-Type", "application/x-tar")
		case strings.HasSuffix(r.URL.Path, "/containers/"+id+"/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
}

// watchHealthBroadcasts subscribes to the runner's hub the way a browser does and
// returns a function waiting for the next container_health_check message. The
// health poll's first tick is 2s after a run reaches it, so the subscription is
// in place well before anything is sent.
func watchHealthBroadcasts(t *testing.T, r *Runner) func() string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(r.hub.HandleWS))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	conn.SetReadLimit(-1)
	return func() string {
		t.Helper()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("waiting for container_health_check: %v", err)
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "container_health_check" {
				return string(data)
			}
		}
	}
}

func createContainerJob(t *testing.T, d *db.DB, containerMode, containerID string) int64 {
	t.Helper()
	destCfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "health-dest-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:            "health-job-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
		Compression:     "none",
		Encryption:      "none",
		ContainerMode:   containerMode,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "container",
		ItemName: containerID,
		ItemID:   containerID,
		Settings: "{}",
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}
	return jobID
}

func assertHealthMessageRedacted(t *testing.T, where, got string) {
	t.Helper()
	for _, secret := range []string{"admin", "hunter2", "token=abc"} {
		if strings.Contains(got, secret) {
			t.Errorf("%s leaks %q: %s", where, secret, got)
		}
	}
	// The verdict and exit code still have to reach the reader.
	if !strings.Contains(got, "exit code 3") {
		t.Errorf("%s lost the cause: %s", where, got)
	}
}

func TestRunJob_PerItemHealthMessageIsRedacted(t *testing.T) {
	const id = "leaky-per-item"
	fakeContainerDocker(t, id, false)
	r, d := newTestRunner(t)
	next := watchHealthBroadcasts(t, r)

	r.RunJob(createContainerJob(t, d, "", id))

	assertHealthMessageRedacted(t, "broadcast", next())
}

func TestRunJob_StopAllHealthMessageIsRedacted(t *testing.T) {
	const id = "leaky-stop-all"
	fakeContainerDocker(t, id, true)
	r, d := newTestRunner(t)
	next := watchHealthBroadcasts(t, r)

	r.RunJob(createContainerJob(t, d, "stop_all", id))

	assertHealthMessageRedacted(t, "broadcast", next())
	entries, err := d.ListActivityLogs(100, "health", 0)
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("health activity = %+v, want one entry", entries)
	}
	assertHealthMessageRedacted(t, "stored activity", entries[0].Details)
}
