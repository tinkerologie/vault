package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewDiscoverHandler checks the constructor returns a non-nil handler.
func TestNewDiscoverHandler(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()
	if h == nil {
		t.Fatal("NewDiscoverHandler returned nil")
	}
}

// TestListContainers_GracefulWhenDockerAbsent checks that the endpoint returns
// 200 with an empty items list when Docker is not available (CI environment).
func TestListContainers_GracefulWhenDockerAbsent(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers", nil)
	w := httptest.NewRecorder()
	h.ListContainers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Whether Docker is available or not, the response must contain "items".
	if _, ok := resp["items"]; !ok {
		t.Errorf("response missing 'items' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestListVMs_GracefulWhenLibvirtAbsent checks that the endpoint returns
// 200 with an empty items list when libvirt is not available (CI / macOS).
func TestListVMs_GracefulWhenLibvirtAbsent(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)
	w := httptest.NewRecorder()
	h.ListVMs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["items"]; !ok {
		t.Errorf("response missing 'items' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestListFolders_Returns200 checks that the endpoint always returns 200.
// On non-Unraid systems the folder handler may return an error, which should
// still yield a graceful 200 with an empty list.
func TestListFolders_Returns200(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/folders", nil)
	w := httptest.NewRecorder()
	h.ListFolders(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["items"]; !ok {
		t.Errorf("response missing 'items' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestListPlugins_GracefulWhenNotUnraid checks that the endpoint returns 200
// when running outside Unraid (no /boot/config/plugins).
func TestListPlugins_GracefulWhenNotUnraid(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	w := httptest.NewRecorder()
	h.ListPlugins(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["items"]; !ok {
		t.Errorf("response missing 'items' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestListZFSDatasets_GracefulWhenZFSAbsent checks that the endpoint returns
// 200 with an empty items list when ZFS is not available.
func TestListZFSDatasets_GracefulWhenZFSAbsent(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/zfs", nil)
	w := httptest.NewRecorder()
	h.ListZFSDatasets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["items"]; !ok {
		t.Errorf("response missing 'items' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestContainerMounts_GracefulWhenDockerAbsent checks that the per-container
// mounts endpoint returns 200 with an empty mounts list when Docker is not
// available (CI / macOS), mirroring the other discover handlers.
func TestContainerMounts_GracefulWhenDockerAbsent(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/sonarr/mounts", nil)
	req = withURLParam(req, "name", "sonarr")
	w := httptest.NewRecorder()
	h.ContainerMounts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["mounts"]; !ok {
		t.Errorf("response missing 'mounts' key: %v", resp)
	}
	if _, ok := resp["available"]; !ok {
		t.Errorf("response missing 'available' key: %v", resp)
	}
}

// TestDiscoverHandlers_ResponseShape validates the consistent response shape
// across all five discover endpoints.
func TestDiscoverHandlers_ResponseShape(t *testing.T) {
	t.Parallel()
	h := NewDiscoverHandler()

	endpoints := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"ListContainers", h.ListContainers},
		{"ListVMs", h.ListVMs},
		{"ListFolders", h.ListFolders},
		{"ListPlugins", h.ListPlugins},
		{"ListZFSDatasets", h.ListZFSDatasets},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/discover", nil)
			w := httptest.NewRecorder()
			ep.handler(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200; body: %s", ep.name, w.Code, w.Body.String())
			}
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("%s: decode: %v", ep.name, err)
			}
			items, ok := resp["items"]
			if !ok {
				t.Errorf("%s: response missing 'items' key: %v", ep.name, resp)
				return
			}
			// items should be a JSON array (possibly empty).
			if _, ok := items.([]any); !ok {
				t.Errorf("%s: items is %T, want []any", ep.name, items)
			}
		})
	}
}
