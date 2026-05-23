package release

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// LatestRelease is the subset of the GitHub releases payload we surface
// to the UI. Marshalled directly as the /api/v1/release/latest response.
type LatestRelease struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"published_at"`
	URL         string    `json:"url"`
}

// Cache fetches and caches the latest release info from GitHub.
// Thread-safe.
type Cache struct {
	baseURL string // "https://api.github.com" — override in tests
	repo    string // "owner/repo"
	ttl     time.Duration
	client  *http.Client

	mu        sync.Mutex
	value     *LatestRelease
	fetchedAt time.Time
}

// NewCache constructs a Cache. baseURL defaults to https://api.github.com
// if empty. Repo is "owner/repo".
func NewCache(baseURL, repo string, ttl time.Duration) *Cache {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Cache{
		baseURL: baseURL,
		repo:    repo,
		ttl:     ttl,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Latest returns the cached latest-release info, refreshing from GitHub
// if the cache is empty or older than ttl. Returns (nil, nil) — not an
// error — when GitHub is unreachable, rate-limited, or returns garbage,
// so the caller can render an offline state without surfacing a daemon
// error to the user.
func (c *Cache) Latest(ctx context.Context) (*LatestRelease, error) {
	c.mu.Lock()
	if c.value != nil && time.Since(c.fetchedAt) < c.ttl {
		v := *c.value
		c.mu.Unlock()
		return &v, nil
	}
	c.mu.Unlock()

	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.baseURL, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("release: build GitHub request: %v", err)
		return nil, nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("release: fetch GitHub: %v", err)
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("release: GitHub returned %d", resp.StatusCode)
		return nil, nil
	}

	var payload struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		HTMLURL     string    `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Printf("release: decode GitHub: %v", err)
		return nil, nil
	}

	rel := &LatestRelease{
		Tag:         payload.TagName,
		PublishedAt: payload.PublishedAt,
		URL:         payload.HTMLURL,
	}
	c.mu.Lock()
	c.value = rel
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	v := *rel
	return &v, nil
}
