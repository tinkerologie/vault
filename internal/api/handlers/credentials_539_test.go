package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/db"
)

const replKey = "peer-api-key-value"

// preserveRedactedSecrets keeps the older tests of this behaviour pointed at its
// new home, credentials.Preserve.
func preserveRedactedSecrets(incoming, existing string) (string, error) {
	return credentials.Preserve(incoming, existing), nil
}

func updateSource(t *testing.T, h *ReplicationHandler, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/replication/x", bytes.NewBufferString(body))
	req = withURLParam(req, "id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	h.Update(w, req)
	return w
}

func seedSource(t *testing.T, d *db.DB, url string) int64 {
	t.Helper()
	id, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "peer", Type: "remote_vault", URL: url, Config: `{"api_key":"` + replKey + `"}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func storedKey(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	src, err := d.GetReplicationSource(id)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.APIKey
}

func TestReplicationResponsesNeverContainTheKey(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	id := seedSource(t, d, "http://peer.lan:24085")

	check := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(w.Body.String(), replKey) {
			t.Errorf("%s leaked the key: %s", name, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/replication", nil))
	check("list", w)
	if !strings.Contains(w.Body.String(), "plain HTTP") {
		t.Errorf("list should warn about plain HTTP: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	h.Get(w, withURLParam(httptest.NewRequest(http.MethodGet, "/replication/x", nil), "id", strconv.FormatInt(id, 10)))
	check("get", w)

	w = httptest.NewRecorder()
	body := `{"name":"new","url":"http://new.lan:24085","config":"{\"api_key\":\"` + replKey + `\"}"}`
	h.Create(w, httptest.NewRequest(http.MethodPost, "/replication", bytes.NewBufferString(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	check("create", w)

	w = updateSource(t, h, id, `{"name":"peer","url":"http://peer.lan:24085","config":"{\"api_key\":\"`+replKey+`\"}"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	check("update", w)
}

func TestReplicationUpdateKeepsOrReplacesTheKey(t *testing.T) {
	t.Parallel()
	const url = "http://peer.lan:24085"
	tests := []struct {
		name, config, want string
	}{
		{"marker keeps", `"{\"api_key\":\"` + credentials.RedactionMarker + `\"}"`, replKey},
		{"blank keeps", `"{\"api_key\":\"\"}"`, replKey},
		{"omitted field keeps", `"{}"`, replKey},
		{"new value replaces", `"{\"api_key\":\"rotated\"}"`, "rotated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, d := setupReplicationTest(t)
			id := seedSource(t, d, url)
			w := updateSource(t, h, id, `{"name":"peer","url":"`+url+`","config":`+tt.config+`}`)
			if w.Code != http.StatusOK {
				t.Fatalf("update: %d %s", w.Code, w.Body.String())
			}
			if got := storedKey(t, d, id); got != tt.want {
				t.Fatalf("stored key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplicationUpdateErrorsAndEmptyStoredKey(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	id := seedSource(t, d, "http://peer.lan:24085")

	// Otherwise a client could redirect the stored key to its own server.
	w := updateSource(t, h, id, `{"name":"peer","url":"http://attacker.example","config":"{\"api_key\":\"`+credentials.RedactionMarker+`\"}"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("URL change with masked key: %d %s", w.Code, w.Body.String())
	}
	w = updateSource(t, h, id, `{"name":"peer","url":"http://attacker.example","config":"{\"api_key\":\"typed-new\"}"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("URL change with new key: %d %s", w.Code, w.Body.String())
	}
	w = updateSource(t, h, id, `{"name":"peer","url":"http://attacker.example","config":"not json"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad config: %d", w.Code)
	}
	if w = updateSource(t, h, 9999, `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing source: %d", w.Code)
	}

	noKey, err := d.CreateReplicationSource(db.ReplicationSource{Name: "open", Type: "remote_vault", URL: "http://open.lan", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	w = updateSource(t, h, noKey, `{"name":"open","url":"http://open.lan","config":"{\"api_key\":\"`+credentials.RedactionMarker+`\"}"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("no stored key: %d %s", w.Code, w.Body.String())
	}
	if got := storedKey(t, d, noKey); got != "" {
		t.Fatalf("marker must not be stored as a key: %q", got)
	}
}

func TestReplicationStoredKeyStillAuthenticates(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-API-Key")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": "1"})
	}))
	defer srv.Close()
	h, d := setupReplicationTest(t)
	d.SetCredentialKey(testSealKey)
	id := seedSource(t, d, srv.URL)
	w := updateSource(t, h, id, `{"name":"peer","url":"`+srv.URL+`","config":"{\"api_key\":\"`+credentials.RedactionMarker+`\"}"}`)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	h.TestConnection(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", strconv.FormatInt(id, 10)))
	if w.Code != http.StatusOK || got != replKey {
		t.Fatalf("code %d, header %q", w.Code, got)
	}
}

var testSealKey = []byte("0123456789abcdef0123456789abcdef")

func TestReplicationLockedKeyIsNeverUsed(t *testing.T) {
	t.Parallel()
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	h, d := setupReplicationTest(t)
	d.SetCredentialKey(testSealKey)
	id := seedSource(t, d, srv.URL)
	d.SetCredentialKey([]byte("ffffffffffffffffffffffffffffffff"))

	w := httptest.NewRecorder()
	h.TestConnection(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", strconv.FormatInt(id, 10)))
	if w.Code != http.StatusConflict || called {
		t.Fatalf("code %d, called %v", w.Code, called)
	}
	w = httptest.NewRecorder()
	h.Get(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", strconv.FormatInt(id, 10)))
	if !strings.Contains(w.Body.String(), `"credentials_locked":true`) || strings.Contains(w.Body.String(), "vault-sealed") {
		t.Fatalf("get = %s", w.Body.String())
	}
}

func TestReplicationTestURLRejectsMarker(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)
	w := httptest.NewRecorder()
	body := `{"url":"http://peer.lan","api_key":"` + credentials.RedactionMarker + `"}`
	h.TestURL(w, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d", w.Code)
	}
}

func TestStorageResponseMasksURLCredentialsAndMalformedConfig(t *testing.T) {
	t.Parallel()
	out := storageResponseWithCapacity(db.StorageDestination{Config: `{"url":"https://bob:pw@dav.example/x","password":"p"}`})
	cfg, _ := out["config"].(string)
	if strings.Contains(cfg, "bob") || strings.Contains(cfg, "pw@") || !strings.Contains(cfg, "https://dav.example/x") {
		t.Fatalf("config = %s", cfg)
	}
	if out["credentials_locked"] != false {
		t.Fatalf("locked = %v", out["credentials_locked"])
	}
	out = storageResponseWithCapacity(db.StorageDestination{Config: `{"password":"s3cret"`})
	if strings.Contains(out["config"].(string), "s3cret") {
		t.Fatalf("malformed config leaked: %v", out["config"])
	}
	out = storageResponseWithCapacity(db.StorageDestination{Config: `{"password":"vault-sealed:v1:abc"}`})
	if out["credentials_locked"] != true || strings.Contains(out["config"].(string), "vault-sealed") {
		t.Fatalf("locked row = %v", out)
	}
}

func TestStorageURLCredentialsSurviveRoundTrip(t *testing.T) {
	t.Parallel()
	stored := `{"url":"https://bob:pw@dav.example/x","password":"p"}`
	merged, err := preserveRedactedSecrets(redactConfig(stored), stored)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "bob:pw@") || !strings.Contains(merged, `"password":"p"`) {
		t.Fatalf("merged = %s", merged)
	}
}

// A pre-upgrade backup holds plaintext and must come back sealed and working. A
// backup from another server holds that server's seals and must report them.
func TestRestoreDBSealsPlaintextAndReportsLockedRows(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	h.db.SetCredentialKey(testSealKey)
	foreign, err := credentials.Seal([]byte("ffffffffffffffffffffffffffffffff"), `{"host":"x","password":"other-server"}`)
	if err != nil {
		t.Fatal(err)
	}
	rel := writePlainDBFixture(t, storageDir, "plain", func(fd *db.DB) {
		if _, err := fd.CreateStorageDestination(db.StorageDestination{Name: "old-plain", Type: "smb", Config: `{"host":"a","password":"restored-secret"}`}); err != nil {
			t.Fatal(err)
		}
		if _, err := fd.CreateStorageDestination(db.StorageDestination{Name: "old-foreign", Type: "smb", Config: foreign}); err != nil {
			t.Fatal(err)
		}
	})
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"storage_path":"`+rel+`"}`)))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"locked_credentials":1`) {
		t.Fatalf("body = %s", w.Body.String())
	}

	var raw string
	if err := h.db.QueryRow(`SELECT config FROM storage_destinations WHERE name='old-plain'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "restored-secret") {
		t.Fatalf("restored plaintext not sealed: %s", raw)
	}
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dests {
		if d.Name == "old-plain" && !strings.Contains(d.Config, "restored-secret") {
			t.Fatalf("restored destination lost access: %s", d.Config)
		}
	}
}

func seedSMB(t *testing.T, d *db.DB) int64 {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{Name: "nas", Type: "smb",
		Config: `{"host":"nas.lan","share":"b","user":"u","password":"stored-pass"}`})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStorageUpdateRefusesMaskedSecretOnNewHost(t *testing.T) {
	t.Parallel()
	h, _, _ := newFileBackedStorageHandler(t)
	id := seedSMB(t, h.db)
	put := func(host, pass string) *httptest.ResponseRecorder {
		cfg, _ := json.Marshal(`{"host":"` + host + `","share":"b","user":"u","password":"` + pass + `"}`)
		w := httptest.NewRecorder()
		h.Update(w, reqWithID(http.MethodPut, "/x", idStr(id), []byte(`{"config":`+string(cfg)+`}`)))
		return w
	}
	if w := put("evil.example", credentials.RedactionMarker); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "re-enter") {
		t.Fatalf("masked on new host: %d %s", w.Code, w.Body.String())
	}
	dest, _ := h.db.GetStorageDestination(id)
	if !strings.Contains(dest.Config, "nas.lan") {
		t.Fatalf("refused edit was saved: %s", dest.Config)
	}
	if w := put("nas.lan", credentials.RedactionMarker); w.Code != http.StatusOK {
		t.Fatalf("masked on same host: %d %s", w.Code, w.Body.String())
	}
	dest, _ = h.db.GetStorageDestination(id)
	if !strings.Contains(dest.Config, "stored-pass") {
		t.Fatalf("stored secret lost: %s", dest.Config)
	}
	if w := put("new.lan", "typed-pass"); w.Code != http.StatusOK {
		t.Fatalf("retyped on new host: %d %s", w.Code, w.Body.String())
	}
}

func TestStorageTestConfigUsesStoredSecretOnlyForSameDestination(t *testing.T) {
	t.Parallel()
	// Records the password it is offered, challenging first as a real server does.
	var got atomic.Value
	got.Store("")
	recordAuth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got.Store(pass)
		w.WriteHeader(http.StatusMultiStatus)
	})
	srv := httptest.NewServer(recordAuth)
	defer srv.Close()
	h, _, _ := newFileBackedStorageHandler(t)
	id, err := h.db.CreateStorageDestination(db.StorageDestination{Name: "dav", Type: "webdav",
		Config: `{"url":"` + srv.URL + `","username":"u","password":"stored-pass"}`})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body map[string]any) string {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		h.TestConfig(w, httptest.NewRequest(http.MethodPost, "/storage/test", bytes.NewReader(raw)))
		return strconv.Itoa(w.Code) + " " + w.Body.String()
	}
	masked := func(url string) string {
		return `{"url":"` + url + `","username":"u","password":"` + credentials.RedactionMarker + `"}`
	}

	post(map[string]any{"id": id, "type": "webdav", "config": masked(srv.URL)})
	if got.Load() != "stored-pass" {
		t.Fatalf("server saw password %q, want the stored one", got.Load())
	}

	got.Store("")
	other := httptest.NewServer(recordAuth)
	defer other.Close()
	if res := post(map[string]any{"id": id, "type": "webdav", "config": masked(other.URL)}); !strings.Contains(res, "re-enter") {
		t.Fatalf("new host: %s", res)
	}
	if got.Load() != "" {
		t.Fatalf("new host received %q", got.Load())
	}
	if res := post(map[string]any{"type": "webdav", "config": masked(other.URL)}); !strings.Contains(res, "enter the password") {
		t.Fatalf("no id: %s", res)
	}
	if got.Load() != "" {
		t.Fatalf("mask sent as a password: %q", got.Load())
	}
	if res := post(map[string]any{"id": 9999, "type": "webdav", "config": masked(other.URL)}); !strings.HasPrefix(res, "404") {
		t.Fatalf("missing row: %s", res)
	}
}
