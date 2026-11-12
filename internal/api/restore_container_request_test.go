package api

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func TestRestoreRequestReviewsSavedContainerEffects(t *testing.T) {
	database := testDB(t)
	server := NewServer(database, ServerConfig{})
	assistant := issueAssistantKey(t, server, "Container assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	job, point, dir := containerRequestFixture(t, database, "", false)
	input := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_type":"container","item_name":"web","write_mode":"replace"}`, job, point)
	response := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, assistant.Key, input)
	var submitted restorerequest.View
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil || response.Code != http.StatusCreated || !submitted.Eligible {
		t.Fatalf("submit container: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "fixture/web:backup") {
		t.Fatal("assistant received saved image details")
	}
	response = callRestoreRequest(server, http.MethodGet, restoreRequestsPath+"/"+submitted.ID, loopbackPeer, "", "")
	for _, effect := range []string{"fixture/web:backup", "stop_existing_container", "remove_existing_container", "recreate_container", "start_container", "load_saved_image"} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), effect) {
			t.Fatalf("missing %s in review: %d %s", effect, response.Code, response.Body)
		}
	}
	if strings.Contains(response.Body.String(), "fixture-do-not-disclose") || strings.Contains(response.Body.String(), dir) {
		t.Fatalf("review exposed raw saved configuration or backup path: %s", response.Body)
	}
}

func containerRequestFixture(t *testing.T, database *db.DB, inspectExtra string, imported bool) (int64, int64, string) {
	t.Helper()
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": root})
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "Container storage", Type: "local", Config: string(requireFixture(t, config, err))})
	job, err := database.CreateJob(db.Job{Name: "Container backup", StorageDestID: requireFixture(t, dest, err)})
	job = requireFixture(t, job, err)
	point := evidencePoint(t, database, job, 0, "full", "private-source/full", fmt.Sprintf(`{"item_sizes":{"web":20},"item_types":{"web":"container"},"imported":%t}`, imported))
	dir := filepath.Join(root, "private-source/full/web")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	save := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	save("config.json", `{"Name":"/web","Config":{"Image":"fixture/web:backup","Env":["PRIVATE_TOKEN=fixture-do-not-disclose"]},"State":{"Running":true}`+inspectExtra+`}`)
	save("image.tar", "fixture image")
	return job, point, dir
}

func TestOperatorApprovesEncryptedContainerAndTracksActualStartup(t *testing.T) {
	database := testDB(t)
	proof := [32]byte{23}
	server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(proof, nil)})
	defer func() { _ = server.Runner().Drain(t.Context()) }()
	assistant := issueAssistantKey(t, server, "Container assistant")
	grantRestoreRequests(t, server, assistant.ID, true)
	job, point, dir := containerRequestFixture(t, database, `,"Mounts":[{"Type":"bind","Source":"/mnt/user/appdata/web/config","Destination":"/config"} ],"HostConfig":{"Binds":["/mnt/user/appdata/web/config:/config"]}`, false)
	const secret = "ephemeral container fixture passphrase"
	configPath := filepath.Join(dir, "config.json")
	config, err := os.ReadFile(configPath)
	config = requireFixture(t, config, err)
	var ciphertext bytes.Buffer
	recipient, err := age.NewScryptRecipient(secret)
	requireFixture(t, recipient, err).SetWorkFactor(10)
	writer, err := age.Encrypt(&ciphertext, recipient)
	writer = requireFixture(t, writer, err)
	if _, err := writer.Write(config); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeContainerFixture(t, configPath+".age", ciphertext.Bytes())
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	const fileContents = "backup configuration contents"
	if err := tarWriter.WriteHeader(&tar.Header{Name: "settings.txt", Mode: 0o600, Uid: os.Getuid(), Gid: os.Getgid(), Size: int64(len(fileContents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(fileContents)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	writeContainerFixture(t, filepath.Join(dir, "volume_0.tar"), archive.Bytes())
	target := filepath.Join(t.TempDir(), "alternate")
	if err := os.MkdirAll(filepath.Join(target, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "config", "unrelated.txt")
	writeContainerFixture(t, sentinel, []byte("replace this"))
	calls := fakeContainerRestoreDaemon(t)
	path, review := submitContainerFixture(t, server, assistant.Key, job, point, target)
	if !review.RequiresMetadataReview || review.Eligible || review.Container != nil {
		t.Fatalf("encrypted metadata unlocked before operator review: %+v", review)
	}
	unsigned := callRestoreRequest(server, http.MethodPost, path+"/review", loopbackPeer, "", `{}`)
	if unsigned.Code != http.StatusForbidden {
		t.Fatalf("unsigned review: %d %s", unsigned.Code, unsigned.Body)
	}
	bad := postContainerAction(t, server, proof, path, "review", review, "incorrect passphrase", "1")
	if bad.Code != http.StatusBadRequest || strings.Contains(bad.Body.String(), "incorrect passphrase") {
		t.Fatalf("wrong review passphrase: %d %s", bad.Code, bad.Body)
	}
	response := postContainerAction(t, server, proof, path, "review", review, secret, "2")
	var unlocked restorerequest.View
	if err := json.Unmarshal(response.Body.Bytes(), &unlocked); err != nil || response.Code != http.StatusOK || !unlocked.Eligible ||
		unlocked.ReviewDigest == review.ReviewDigest || unlocked.Container == nil || len(unlocked.Container.Volumes) != 1 ||
		unlocked.Container.Volumes[0].WriteMode != "replace" || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("operator review: %d %s", response.Code, response.Body)
	}
	if len(calls()) != 0 {
		t.Fatalf("review mutated Docker: %v", calls())
	}
	bad = postContainerAction(t, server, proof, path, "approve", unlocked, "incorrect passphrase", "3")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong approval passphrase: %d %s", bad.Code, bad.Body)
	}
	response = postContainerAction(t, server, proof, path, "approve", unlocked, secret, "4")
	if response.Code != http.StatusAccepted {
		t.Fatalf("approve: %d %s", response.Code, response.Body)
	}
	result := waitForContainerRestore(t, server, path, assistant.Key)
	if result.Status != "completed" || result.Run == nil || result.Run.ApplicationHealth != "unchecked" ||
		!slices.Contains(result.Run.ReadinessChecks, engine.ContainerStarted) || result.Container != nil {
		t.Fatalf("assistant outcome: %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(target, "config", "settings.txt"))
	if err != nil || string(body) != fileContents {
		t.Fatalf("restored volume contents %q: %v", body, err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("replace retained unrelated data: %v", err)
	}
	if !slices.Equal(calls(), []string{"image_load", "stop", "remove", "create", "start"}) {
		t.Fatalf("Docker mutations: %v", calls())
	}
}

func TestContainerRequestRefusesChangedEffectsAndManualOverrides(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(fmt.Sprintf("imported=%t", imported), func(t *testing.T) {
			database := testDB(t)
			proof := [32]byte{24}
			server := NewServer(database, ServerConfig{OperatorProof: operatorproof.New(proof, nil)})
			assistant := issueAssistantKey(t, server, "Container assistant")
			grantRestoreRequests(t, server, assistant.ID, true)
			extra := ""
			if imported {
				extra = `,"HostConfig":{"Privileged":true}`
			}
			job, point, dir := containerRequestFixture(t, database, extra, imported)
			calls := fakeContainerRestoreDaemon(t)
			path, review := submitContainerFixture(t, server, assistant.Key, job, point, "")
			if imported {
				if review.Eligible || !review.ManualRestoreRequired {
					t.Fatalf("privileged imported config eligible: %+v", review)
				}
				response := postContainerAction(t, server, proof, path, "approve", review, "", "1")
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "manual Restore wizard") {
					t.Fatalf("override refusal: %d %s", response.Code, response.Body)
				}
			} else {
				config := filepath.Join(dir, "config.json")
				info, err := os.Stat(config)
				info = requireFixture(t, info, err)
				original, err := os.ReadFile(config)
				// Preserve the size and mtime so this tests the saved effect
				// fingerprint independently of storage metadata.
				writeContainerFixture(t, config, bytes.Replace(requireFixture(t, original, err), []byte(`"Running":true`), []byte(`"Running":null`), 1))
				if err := os.Chtimes(config, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				response := postContainerAction(t, server, proof, path, "approve", review, "", "2")
				if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "reviewed_operation_changed") {
					t.Fatalf("changed effects: %d %s", response.Code, response.Body)
				}
			}
			if len(calls()) != 0 {
				t.Fatalf("refused operation mutated Docker: %v", calls())
			}
		})
	}
}

func submitContainerFixture(t *testing.T, server *Server, key string, job, point int64, destination string) (string, restorerequest.View) {
	t.Helper()
	input := fmt.Sprintf(`{"job_id":%d,"restore_point_id":%d,"item_type":"container","item_name":"web","write_mode":"replace","destination":%q}`, job, point, destination)
	response := callRestoreRequest(server, http.MethodPost, restoreRequestsPath, remotePeer, key, input)
	var view restorerequest.View
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", response.Code, response.Body)
	}
	path := restoreRequestsPath + "/" + view.ID
	response = callRestoreRequest(server, http.MethodGet, path, loopbackPeer, "", "")
	view = restorerequest.View{}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusOK || view.ReviewDigest == "" {
		t.Fatalf("review: %d %s", response.Code, response.Body)
	}
	return path, view
}

func postContainerAction(t *testing.T, server *Server, proof [32]byte, path, action string, review restorerequest.View, passphrase, nonce string) *httptest.ResponseRecorder {
	t.Helper()
	return callRestoreRequest(server, http.MethodPost, path+"/"+action, loopbackPeer, "", signedApproval(t, proof, operatorproof.Action{
		Version: 1, Action: action, ID: review.ID, ReviewDigest: review.ReviewDigest, Passphrase: passphrase,
		Timestamp: time.Now().Unix(), Nonce: strings.Repeat(nonce, 32), Operator: "root",
	}))
}

func waitForContainerRestore(t *testing.T, server *Server, path, key string) restorerequest.View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var view restorerequest.View
		response := callRestoreRequest(server, http.MethodGet, path, remotePeer, key, "")
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusOK {
			t.Fatalf("follow: %d %s", response.Code, response.Body)
		}
		if view.Status != "running" {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("container Restore Run did not finish: %s", response.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeContainerFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeContainerRestoreDaemon(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	running := true
	record := func(call string) { calls = append(calls, call) }
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		if strings.HasPrefix(p, "/v1.") {
			_, path, _ := strings.Cut(p[1:], "/")
			p = "/" + path
		}
		switch {
		case p == "/_ping":
			w.Header().Set("API-Version", "1.53")
			w.Header().Set("OSType", "linux")
		case r.Method == http.MethodPost && p == "/images/load":
			_, _ = io.Copy(io.Discard, r.Body)
			record("image_load")
			_, _ = fmt.Fprintln(w, `{"stream":"Loaded image: fixture/web:backup"}`)
		case p == "/containers/web/json":
			_, _ = fmt.Fprintf(w, `{"Id":"original-web","State":{"Running":%t}}`, running)
		case p == "/containers/original-web/stop":
			record("stop")
			running = false
			w.WriteHeader(http.StatusNoContent)
		case p == "/containers/original-web" && r.Method == http.MethodDelete:
			record("remove")
			w.WriteHeader(http.StatusNoContent)
		case p == "/containers/create":
			record("create")
			_, _ = fmt.Fprintln(w, `{"Id":"restored-web"}`)
		case p == "/containers/restored-web/start":
			record("start")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}
