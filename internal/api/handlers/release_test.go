package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/release"
)

// fakeLatest implements latestFetcher for tests.
type fakeLatest struct {
	rel *release.LatestRelease
}

func (f *fakeLatest) Latest(_ context.Context) (*release.LatestRelease, error) {
	return f.rel, nil
}

const sampleChangelog = `## [v1.0.0] - 2026-05-23

### Added

- thing
`

func TestReleaseChangelog(t *testing.T) {
	h := NewReleaseHandler(sampleChangelog, &fakeLatest{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/release/changelog", nil)
	w := httptest.NewRecorder()
	h.Changelog(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct{ Releases []release.Release }
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Releases) != 1 || body.Releases[0].Version != "v1.0.0" {
		t.Errorf("got %+v", body.Releases)
	}
}

func TestReleaseChangelogEmptyChangelogReturnsEmptyArray(t *testing.T) {
	// Empty changelog should produce [] not null — the frontend calls
	// .length on the array, null would break it.
	h := NewReleaseHandler("", &fakeLatest{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/release/changelog", nil)
	w := httptest.NewRecorder()
	h.Changelog(w, req)
	if !strings.Contains(w.Body.String(), `"releases":[]`) {
		t.Errorf("expected empty array, got body = %s", w.Body.String())
	}
}

func TestReleaseLatestSuccess(t *testing.T) {
	rel := &release.LatestRelease{Tag: "v1.2.3", PublishedAt: time.Now(), URL: "https://example"}
	h := NewReleaseHandler(sampleChangelog, &fakeLatest{rel: rel})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/release/latest", nil)
	w := httptest.NewRecorder()
	h.Latest(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"tag":"v1.2.3"`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestReleaseLatestUnavailable(t *testing.T) {
	h := NewReleaseHandler(sampleChangelog, &fakeLatest{rel: nil})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/release/latest", nil)
	w := httptest.NewRecorder()
	h.Latest(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}
