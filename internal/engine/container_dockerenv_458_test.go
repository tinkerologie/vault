package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDockerAPI458 is a loopback HTTP server speaking just enough of the
// Docker Engine API for the package-level helpers that build their own
// client from the environment (StopContainers, StartContainers,
// VerifyContainerHealth). Containers are keyed by ID; an unknown ID is a 404.
type fakeDockerAPI458 struct {
	mu         sync.Mutex
	states     map[string]map[string]any // id -> State object (nil value: no State)
	stopFail   map[string]bool
	startFail  map[string]bool
	stopped    []string
	started    []string
	inspectHit map[string]int
}

func newFakeDockerAPI458(t *testing.T) *fakeDockerAPI458 {
	t.Helper()
	api := &fakeDockerAPI458{
		states:     map[string]map[string]any{},
		stopFail:   map[string]bool{},
		startFail:  map[string]bool{},
		inspectHit: map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	return api
}

func (a *fakeDockerAPI458) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Api-Version", "1.47")
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if strings.HasSuffix(path, "/_ping") {
		_, _ = w.Write([]byte("OK"))
		return
	}
	i := strings.Index(path, "/containers/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	rest := strings.Split(strings.TrimPrefix(path[i:], "/containers/"), "/")
	if len(rest) != 2 {
		http.NotFound(w, r)
		return
	}
	id, action := rest[0], rest[1]

	a.mu.Lock()
	defer a.mu.Unlock()
	state, known := a.states[id]
	if !known {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: ` + id + `"}`))
		return
	}
	switch action {
	case "json":
		a.inspectHit[id]++
		body := map[string]any{"Id": id, "Name": "/" + id}
		if state != nil {
			body["State"] = state
		}
		_ = json.NewEncoder(w).Encode(body)
	case "stop":
		if a.stopFail[id] {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"cannot stop"}`))
			return
		}
		a.stopped = append(a.stopped, id)
		w.WriteHeader(http.StatusNoContent)
	case "start":
		if a.startFail[id] {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"cannot start"}`))
			return
		}
		a.started = append(a.started, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (a *fakeDockerAPI458) set(id string, state map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.states[id] = state
}

func TestStopContainers458(t *testing.T) {
	api := newFakeDockerAPI458(t)
	api.set("web", map[string]any{"Running": true, "Status": "running"})
	api.set("idle", map[string]any{"Running": false, "Status": "exited"})
	api.set("db", map[string]any{"Running": true, "Status": "running"})

	stopped, err := StopContainers([]string{"web", "idle", "db"})
	if err != nil {
		t.Fatalf("StopContainers: %v", err)
	}
	if strings.Join(stopped, ",") != "web,db" {
		t.Errorf("stopped = %v, want only the running ones", stopped)
	}

	// A stop failure returns what was stopped so far.
	api.mu.Lock()
	api.stopFail["db"] = true
	api.mu.Unlock()
	stopped, err = StopContainers([]string{"web", "db"})
	if err == nil || !strings.Contains(err.Error(), "stopping container db") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(stopped, ",") != "web" {
		t.Errorf("stopped before failure = %v", stopped)
	}

	// An unknown container fails the inspect.
	if _, err := StopContainers([]string{"ghost"}); err == nil || !strings.Contains(err.Error(), "inspecting container ghost") {
		t.Errorf("err = %v", err)
	}
}

func TestStartContainers458(t *testing.T) {
	api := newFakeDockerAPI458(t)
	api.set("a", map[string]any{})
	api.set("b", map[string]any{})
	api.mu.Lock()
	api.startFail["b"] = true
	api.mu.Unlock()

	errs := StartContainers([]string{"a", "b", "a"})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "starting container b") {
		t.Fatalf("errs = %v", errs)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if strings.Join(api.started, ",") != "a,a" {
		t.Errorf("a failure must not stop the remaining starts: %v", api.started)
	}
}

func TestDockerHelpersInvalidHost458(t *testing.T) {
	// No scheme separator: rejected while parsing, before any connection.
	t.Setenv("DOCKER_HOST", "not-a-docker-host")
	if _, err := StopContainers([]string{"x"}); err == nil || !strings.Contains(err.Error(), "creating docker client") {
		t.Errorf("StopContainers err = %v", err)
	}
	if errs := StartContainers([]string{"x"}); len(errs) != 1 || !strings.Contains(errs[0].Error(), "creating docker client") {
		t.Errorf("StartContainers errs = %v", errs)
	}
	if _, err := VerifyContainerHealth("x", "x", time.Second); err == nil || !strings.Contains(err.Error(), "creating docker client") {
		t.Errorf("VerifyContainerHealth err = %v", err)
	}
	if _, err := NewContainerHandler(); err == nil {
		t.Error("NewContainerHandler should reject an invalid DOCKER_HOST")
	}
}

// VerifyContainerHealth polls every 2s, so every case that inspects at all
// waits one tick. The cases share a server and run in parallel, keeping the
// whole test to roughly one poll interval plus the longest timeout.
func TestVerifyContainerHealth458(t *testing.T) {
	api := newFakeDockerAPI458(t)
	api.set("running", map[string]any{"Running": true, "Status": "running"})
	api.set("healthy", map[string]any{"Running": true, "Health": map[string]any{"Status": "healthy"}})
	api.set("unhealthy", map[string]any{"Running": true, "Health": map[string]any{"Status": "unhealthy"}})
	api.set("starting", map[string]any{"Running": true, "Health": map[string]any{"Status": "starting"}})
	api.set("exited", map[string]any{"Running": false, "Status": "exited", "ExitCode": 137})
	api.set("restarting", map[string]any{"Running": false, "Restarting": true, "Status": "restarting"})
	api.set("nostate", nil)

	tests := []struct {
		id         string
		timeout    time.Duration
		wantStatus string
		wantMsg    string
	}{
		{id: "running", timeout: 5 * time.Second, wantStatus: "running", wantMsg: "no HEALTHCHECK"},
		{id: "healthy", timeout: 5 * time.Second, wantStatus: "healthy"},
		{id: "unhealthy", timeout: 5 * time.Second, wantStatus: "unhealthy"},
		{id: "exited", timeout: 5 * time.Second, wantStatus: "failed", wantMsg: "exit code 137"},
		// These keep polling and end on the timeout.
		{id: "starting", timeout: 2500 * time.Millisecond, wantStatus: "failed", wantMsg: "Timed out"},
		{id: "restarting", timeout: 2500 * time.Millisecond, wantStatus: "failed", wantMsg: "Timed out"},
		{id: "nostate", timeout: 2500 * time.Millisecond, wantStatus: "failed", wantMsg: "Timed out"},
		{id: "ghost", timeout: 2500 * time.Millisecond, wantStatus: "failed", wantMsg: "Timed out"},
		// Shorter than one tick: never inspects.
		{id: "running", timeout: 10 * time.Millisecond, wantStatus: "failed", wantMsg: "Timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.id+"/"+tt.timeout.String(), func(t *testing.T) {
			t.Parallel()
			res, err := VerifyContainerHealth(tt.id, "name-"+tt.id, tt.timeout)
			if err != nil {
				t.Fatalf("VerifyContainerHealth: %v", err)
			}
			if res.Status != tt.wantStatus || !strings.Contains(res.Message, tt.wantMsg) || res.ContainerName != "name-"+tt.id {
				t.Errorf("result = %+v, want status %q message ~%q", res, tt.wantStatus, tt.wantMsg)
			}
		})
	}
}
