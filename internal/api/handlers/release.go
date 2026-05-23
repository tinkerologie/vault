package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/ruaan-deysel/vault/internal/release"
)

// latestFetcher is the slice of *release.Cache the handler needs. Defined
// as an interface so tests can inject a stub without spinning up an HTTP
// server.
type latestFetcher interface {
	Latest(ctx context.Context) (*release.LatestRelease, error)
}

// ReleaseHandler serves the About card endpoints:
//
//	GET /api/v1/release/changelog
//	GET /api/v1/release/latest
type ReleaseHandler struct {
	changelog string
	latest    latestFetcher
}

// NewReleaseHandler constructs a handler bound to a static changelog
// string (the embedded CHANGELOG.md) and a latest-release fetcher
// (typically *release.Cache).
func NewReleaseHandler(changelog string, latest latestFetcher) *ReleaseHandler {
	return &ReleaseHandler{changelog: changelog, latest: latest}
}

// Changelog responds with the parsed bundled changelog.
//
//	GET /api/v1/release/changelog
//	200 { "releases": [Release...] }
func (h *ReleaseHandler) Changelog(w http.ResponseWriter, r *http.Request) {
	releases, _ := release.Parse(h.changelog)
	// Cap to a sensible upper bound so a misshapen changelog can't OOM
	// the response. Modal renders the latest expanded; older entries
	// rarely matter.
	const maxReleases = 50
	if len(releases) > maxReleases {
		releases = releases[:maxReleases]
	}
	if releases == nil {
		releases = []release.Release{}
	}
	resp := map[string]any{"releases": releases}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("release: encode changelog: %v", err)
	}
}

// Latest responds with the latest release tag from GitHub, or 204 when
// GitHub is unreachable.
//
//	GET /api/v1/release/latest
//	200 LatestRelease | 204 No Content
func (h *ReleaseHandler) Latest(w http.ResponseWriter, r *http.Request) {
	rel, _ := h.latest.Latest(r.Context())
	if rel == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rel); err != nil {
		log.Printf("release: encode latest: %v", err)
	}
}
