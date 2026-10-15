package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var dockerVersionPrefix458 = regexp.MustCompile(`^/v[0-9.]+`)

// fakeDocker458 starts a loopback HTTP server speaking the small subset of the
// Docker Engine API the discovery and preset handlers use, and points the
// moby client at it via DOCKER_HOST. Tests using it must not call
// t.Parallel (t.Setenv enforces this).
func fakeDocker458(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := dockerVersionPrefix458.ReplaceAllString(r.URL.Path, "")
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Ostype", "linux")
		switch {
		case p == "/_ping":
			_, _ = w.Write([]byte("OK"))
		case p == "/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"0123456789abcdef0123","Names":["/web"],"Image":"ghcr.io/immich-app/immich-server","State":"running","Labels":{"vault.exclude":"/cache"}},
				{"Id":"fedcba98765432100000","Names":[],"Image":"postgres:16","State":"exited"}]`))
		case p == "/containers/web/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"0123456789abcdef0123","Name":"/web","Config":{"Labels":{}},
				"Mounts":[{"Type":"bind","Source":"/var/run/docker.sock","Destination":"/var/run/docker.sock"},
				{"Type":"bind","Source":"/var/run/other.sock","Destination":""},
				{"Type":"bind","Source":"/mnt/user/appdata/web","Destination":"/config"}]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container"}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
}

func decodeMap458(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return m
}

func TestDiscover458_FakeDocker(t *testing.T) {
	fakeDocker458(t)

	t.Run("list containers", func(t *testing.T) {
		w := httptest.NewRecorder()
		NewDiscoverHandler(nil).ListContainers(w, httptest.NewRequest(http.MethodGet, "/containers", nil))
		wantStatus458(t, w, http.StatusOK)
		m := decodeMap458(t, w)
		if m["available"] != true {
			t.Fatalf("available = %v; body %s", m["available"], w.Body.String())
		}
		if items, _ := m["items"].([]any); len(items) != 2 {
			t.Errorf("items = %v", m["items"])
		}
	})

	t.Run("container mounts with db", func(t *testing.T) {
		d := newTestDB(t)
		w := httptest.NewRecorder()
		NewDiscoverHandler(d).ContainerMounts(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "name", "web"))
		wantStatus458(t, w, http.StatusOK)
		if m := decodeMap458(t, w); m["available"] != true {
			t.Fatalf("body %s", w.Body.String())
		}
	})

	t.Run("container mounts with closed db falls back to defaults", func(t *testing.T) {
		d := newTestDB(t)
		_ = d.Close()
		w := httptest.NewRecorder()
		NewDiscoverHandler(d).ContainerMounts(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "name", "web"))
		wantStatus458(t, w, http.StatusOK)
		if m := decodeMap458(t, w); m["available"] != true {
			t.Fatalf("body %s", w.Body.String())
		}
	})

	t.Run("container mounts with blank appdata setting", func(t *testing.T) {
		d := newTestDB(t)
		if err := d.SetSetting("appdata_path", ""); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		NewDiscoverHandler(d).ContainerMounts(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "name", "web"))
		wantStatus458(t, w, http.StatusOK)
	})

	t.Run("unknown container", func(t *testing.T) {
		w := httptest.NewRecorder()
		NewDiscoverHandler(nil).ContainerMounts(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "name", "ghost"))
		wantStatus458(t, w, http.StatusOK)
		if m := decodeMap458(t, w); m["available"] != false || m["error"] == nil {
			t.Fatalf("body %s", w.Body.String())
		}
	})

	t.Run("preset exclusions include socket mounts and metadata", func(t *testing.T) {
		w := httptest.NewRecorder()
		NewPresetsHandler().GetExclusions(w, httptest.NewRequest(http.MethodGet, "/presets/exclusions?image=immich-app/immich-server&container=web", nil))
		wantStatus458(t, w, http.StatusOK)
		m := decodeMap458(t, w)
		paths, _ := m["paths"].([]any)
		found := false
		for _, p := range paths {
			if p == "/var/run/docker.sock" {
				found = true
			}
		}
		if !found {
			t.Errorf("socket mount not suggested: %v", paths)
		}
		if m["notes"] == nil || m["warnings"] == nil {
			t.Errorf("missing preset metadata: %v", m)
		}
	})
}

func TestDiscover458_DockerClientConfigError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "not a valid host::")
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"containers": NewDiscoverHandler(nil).ListContainers,
		"mounts":     NewDiscoverHandler(nil).ContainerMounts,
	} {
		w := httptest.NewRecorder()
		fn(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "name", "web"))
		wantStatus458(t, w, http.StatusOK)
		if m := decodeMap458(t, w); m["available"] != false {
			t.Errorf("%s: body %s", name, w.Body.String())
		}
	}
}
