package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPresetsHandler(t *testing.T) {
	h := NewPresetsHandler()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestPresetsGetExclusions_NoParams(t *testing.T) {
	h := NewPresetsHandler()

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/presets/exclusions", nil)
	h.GetExclusions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Keys must always be present.
	if _, ok := resp["paths"]; !ok {
		t.Error("response missing 'paths' key")
	}
	if _, ok := resp["image"]; !ok {
		t.Error("response missing 'image' key")
	}
	if _, ok := resp["container"]; !ok {
		t.Error("response missing 'container' key")
	}

	// No image → no preset paths returned.
	paths, ok := resp["paths"].([]any)
	if !ok {
		t.Fatalf("paths type %T, want []any", resp["paths"])
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths for empty image, got %d", len(paths))
	}
}

func TestPresetsGetExclusions_KnownImage(t *testing.T) {
	h := NewPresetsHandler()

	tests := []struct {
		image   string
		wantMin int // minimum number of preset paths
	}{
		{image: "plex", wantMin: 3},
		{image: "jellyfin", wantMin: 2},
		{image: "sonarr", wantMin: 2},
		{image: "homeassistant", wantMin: 3},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := newReq(http.MethodGet, "/api/v1/presets/exclusions?image="+tt.image, nil)
			h.GetExclusions(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}

			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}

			paths, ok := resp["paths"].([]any)
			if !ok {
				t.Fatalf("paths type %T, want []any", resp["paths"])
			}
			if len(paths) < tt.wantMin {
				t.Errorf("image %q: got %d paths, want >= %d", tt.image, len(paths), tt.wantMin)
			}

			if resp["image"] != tt.image {
				t.Errorf("image field = %q, want %q", resp["image"], tt.image)
			}
		})
	}
}

func TestPresetsGetExclusions_UnknownImage(t *testing.T) {
	h := NewPresetsHandler()

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=someunknownimage12345", nil)
	h.GetExclusions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	paths, ok := resp["paths"].([]any)
	if !ok {
		t.Fatalf("paths type %T, want []any", resp["paths"])
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths for unknown image, got %d", len(paths))
	}
}

func TestPresetsGetExclusions_CaseInsensitiveMatch(t *testing.T) {
	h := NewPresetsHandler()

	// "Plex" (capital P) must still match the "plex" preset.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=linuxserver%2FPlex", nil)
	h.GetExclusions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	paths, ok := resp["paths"].([]any)
	if !ok {
		t.Fatalf("paths type %T", resp["paths"])
	}
	if len(paths) == 0 {
		t.Error("expected preset paths for 'linuxserver/Plex', got none")
	}
}

func TestPresetsGetExclusions_ImageInFullRef(t *testing.T) {
	h := NewPresetsHandler()

	// Full image reference with tag — should still match "sonarr" substring.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=linuxserver%2Fsonarr%3Alatest", nil)
	h.GetExclusions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	paths, ok := resp["paths"].([]any)
	if !ok {
		t.Fatalf("paths type %T", resp["paths"])
	}
	if len(paths) == 0 {
		t.Error("expected preset paths for 'linuxserver/sonarr:latest', got none")
	}
}

func TestPresetsGetExclusions_ContainerParam(t *testing.T) {
	h := NewPresetsHandler()

	// Supply a container name; Docker is not available in test environment, so
	// DetectSocketMounts will fail and the handler must still return 200 with
	// whatever static preset paths are available for the image.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=plex&container=nonexistent-test-container", nil)
	h.GetExclusions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// container field in response must match what was sent.
	if resp["container"] != "nonexistent-test-container" {
		t.Errorf("container = %v, want 'nonexistent-test-container'", resp["container"])
	}

	// Plex image presets must still be returned regardless of Docker error.
	paths, ok := resp["paths"].([]any)
	if !ok {
		t.Fatalf("paths type %T", resp["paths"])
	}
	if len(paths) == 0 {
		t.Error("expected plex preset paths even when Docker is unavailable")
	}
}

func TestPresetsGetExclusions_DuplicatePrevention(t *testing.T) {
	h := NewPresetsHandler()

	// Two requests for the same image should return the same de-duped paths.
	w1 := httptest.NewRecorder()
	r1 := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=plex", nil)
	h.GetExclusions(w1, r1)

	w2 := httptest.NewRecorder()
	r2 := newReq(http.MethodGet, "/api/v1/presets/exclusions?image=plex", nil)
	h.GetExclusions(w2, r2)

	var resp1, resp2 map[string]any
	json.NewDecoder(w1.Body).Decode(&resp1) //nolint:errcheck
	json.NewDecoder(w2.Body).Decode(&resp2) //nolint:errcheck

	p1 := resp1["paths"].([]any)
	p2 := resp2["paths"].([]any)

	if len(p1) != len(p2) {
		t.Errorf("path counts differ between identical requests: %d vs %d", len(p1), len(p2))
	}
}
