package api

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/webdav"

	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/runner"
)

// The restore wizard stops its own Restore Run through the same exact-run
// endpoint as an approved Restore Request (#701): only the signed-in operator,
// only the run ID that is active, and never a backup run.
func TestStopRestoreRunStopsExactWizardRestore(t *testing.T) {
	database := testDB(t)
	job, _, root, target := requestFolderFixture(t, database)
	archive, err := os.Create(filepath.Join(root, "private-source/full/archive/data.tar"))
	requireFixture(t, archive, err)
	writer := tar.NewWriter(archive)
	if err := writer.WriteHeader(&tar.Header{Name: "restored.txt", Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archive.Name())
	data = requireFixture(t, data, err)
	point := evidencePoint(t, database, job, 0, "full", "private-source/full", fmt.Sprintf(
		`{"item_sizes":{"archive":20},"item_types":{"archive":"folder"},"checksums":{"archive":{"data.tar":"%x"}}}`, sha256.Sum256(data)))

	entered, release := make(chan struct{}), make(chan struct{})
	enterOnce, releaseOnce := sync.OnceFunc(func() { close(entered) }), sync.OnceFunc(func() { close(release) })
	files := webdav.Handler{FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/data.tar") {
			enterOnce()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		files.ServeHTTP(w, r)
	}))
	defer backup.Close()
	defer releaseOnce() // runs before Close, so a failed test never blocks on the held read
	j, err := database.GetJob(job)
	j = requireFixture(t, j, err)
	dest, err := database.GetStorageDestination(j.StorageDestID)
	dest = requireFixture(t, dest, err)
	dest.Type, dest.Config = "webdav", fmt.Sprintf(`{"url":%q}`, backup.URL)
	if err := database.UpdateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}

	key := [32]byte{91}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(key, nil)})
	defer server.mcp.Close()
	nonce := 0
	sign := func(id string) string {
		nonce++
		return signedApproval(t, key, operatorproof.Action{
			Version: 1, Action: "stop", ID: id, Operator: "wizard-operator",
			Nonce: fmt.Sprintf("%032x", nonce), Timestamp: time.Now().Unix(),
		})
	}

	restorePoint, err := database.GetRestorePoint(point)
	restorePoint = requireFixture(t, restorePoint, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Runner().RunRestore(restorePoint, []runner.RestoreTarget{{Name: "archive", Type: "folder", CleanDestination: true}}, "", "")
	}()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("restore did not reach its storage read")
	}
	runID := server.Runner().Status().RunID
	if runID == 0 {
		t.Fatal("no active Restore Run")
	}
	id := strconv.FormatInt(runID, 10)

	other := strconv.FormatInt(runID+1, 10)
	if res := callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/"+other+"/stop", loopbackPeer, "", sign(other)); res.Code != http.StatusNotFound {
		t.Fatalf("stop of another run: %d %s", res.Code, res.Body)
	}
	if res := callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/"+id+"/stop", loopbackPeer, "", ""); res.Code != http.StatusForbidden {
		t.Fatalf("stop without proof: %d %s", res.Code, res.Body)
	}
	res := callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/"+id+"/stop", loopbackPeer, "", sign(id))
	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"status":"stopping"`) {
		t.Fatalf("stop: %d %s", res.Code, res.Body)
	}
	releaseOnce()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("stopped restore did not finish")
	}
	server.Runner().WaitForBackgroundWork()

	run, err := database.GetJobRun(runID)
	run = requireFixture(t, run, err)
	if run.Status != "cancelled" || run.StoppedBy != "wizard-operator" || run.StopRequestedAt == nil {
		t.Fatalf("stopped run: status=%q stopped_by=%q at=%v", run.Status, run.StoppedBy, run.StopRequestedAt)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); !os.IsNotExist(err) {
		t.Fatalf("stopped restore changed the target: %v", err)
	}

	// A repeated stop after the run ended reports its recorded outcome.
	res = callRestoreRequest(server, http.MethodPost, "/api/v1/restore-runs/"+id+"/stop", loopbackPeer, "", sign(id))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("late stop: %d %s", res.Code, res.Body)
	}
}
