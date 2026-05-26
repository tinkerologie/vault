package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// mockZFSLister returns a fixed list of ZFS mountpoints.
type mockZFSLister struct {
	mounts []ZFSMountInfo
	err    error
}

func (m *mockZFSLister) ListZFSMountpoints() ([]ZFSMountInfo, error) {
	return m.mounts, m.err
}

// TestNewBrowseHandler checks the constructor returns a non-nil handler.
func TestNewBrowseHandler(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	if h == nil {
		t.Fatal("NewBrowseHandler returned nil")
	}
}

// TestSetZFSLister_Nil checks that nil lister does not panic.
func TestSetZFSLister_Nil(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	h.SetZFSLister(nil) // should not panic
}

// TestSetZFSLister_Error checks that a lister returning an error is not stored.
func TestSetZFSLister_Error(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	lister := &mockZFSLister{err: os.ErrPermission}
	h.SetZFSLister(lister)
	// zfsLister should remain nil since the first call returned an error.
	if h.zfsLister != nil {
		t.Error("zfsLister should remain nil when ListZFSMountpoints errors")
	}
}

// TestSetZFSLister_Success checks that a successful lister is stored and roots are merged.
func TestSetZFSLister_Success(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	lister := &mockZFSLister{
		mounts: []ZFSMountInfo{
			{Name: "tank", Mountpoint: "/mnt/tank"},
			{Name: "pool2", Mountpoint: "/mnt/pool2"},
		},
	}
	h.SetZFSLister(lister)
	if h.zfsLister == nil {
		t.Error("zfsLister should be set after successful ListZFSMountpoints call")
	}
	if len(h.extraAllowedRoots) != 2 {
		t.Errorf("extraAllowedRoots len = %d, want 2", len(h.extraAllowedRoots))
	}
}

// TestMergeExtraRoots_Dedup tests that duplicate entries are not added.
func TestMergeExtraRoots_Dedup(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	mounts := []ZFSMountInfo{
		{Name: "tank", Mountpoint: "/mnt/tank"},
		{Name: "tank2", Mountpoint: "/mnt/tank"}, // duplicate path
		{Name: "pool2", Mountpoint: "/mnt/pool2"},
		{Name: "bad", Mountpoint: ""},          // empty — skip
		{Name: "bad2", Mountpoint: "/"},        // root — skip
		{Name: "bad3", Mountpoint: "relative"}, // relative — skip
	}
	h.mergeExtraRoots(mounts)
	if len(h.extraAllowedRoots) != 2 {
		t.Errorf("extraAllowedRoots = %v (len %d), want 2 entries", h.extraAllowedRoots, len(h.extraAllowedRoots))
	}
}

// TestBrowseList_NoPath_ReturnsRoots checks that browsing with no path returns
// an object with "entries" (list). We use the real filesystem so that entries
// is whatever /mnt actually exists (may be empty — that's fine).
func TestBrowseList_NoPath_ReturnsRoots(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["entries"]; !ok {
		t.Errorf("response missing 'entries' key: %v", resp)
	}
	if _, ok := resp["path"]; !ok {
		t.Errorf("response missing 'path' key: %v", resp)
	}
}

// TestBrowseList_WithPath_TempDir validates listing a real directory.
func TestBrowseList_WithPath_TempDir(t *testing.T) {
	t.Parallel()

	// We need a path under an allowed root. The browse handler only allows
	// /mnt and /boot by default. We work around this by adding the temp
	// dir's parent to the handler's extra allowed roots.
	base := t.TempDir()
	// Create a subdirectory and a file inside the temp dir.
	subDir := filepath.Join(base, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	filePath := filepath.Join(base, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Give the handler an extra allowed root for the temp dir.
	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path="+base, nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries, ok := resp["entries"].([]any)
	if !ok {
		t.Fatalf("entries not a list: %T", resp["entries"])
	}
	// Only the directory should appear (files excluded by default).
	if len(entries) != 1 {
		t.Errorf("entries len = %d, want 1 (dir only); entries: %v", len(entries), entries)
	}
}

// TestBrowseList_WithPath_IncludeFiles checks ?files=true returns files too.
func TestBrowseList_WithPath_IncludeFiles(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "myfile.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path="+base+"&files=true", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries, ok := resp["entries"].([]any)
	if !ok {
		t.Fatalf("entries not a list: %T", resp["entries"])
	}
	// Both the dir and the file should appear.
	if len(entries) != 2 {
		t.Errorf("entries len = %d, want 2; entries: %v", len(entries), entries)
	}
}

// TestBrowseList_ForbiddenPath checks that a path outside allowed roots returns 403.
func TestBrowseList_ForbiddenPath(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()

	// /tmp is not in the allowed roots (/mnt, /boot).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path=/tmp", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", w.Code, w.Body.String())
	}
}

// TestBrowseList_ZFS_IncludeZFS checks that ZFS mounts are returned with include_zfs=true.
func TestBrowseList_ZFS_IncludeZFS(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	// Provide a ZFS lister with a made-up but absolute mountpoint. The
	// lister is assigned only if the first call succeeds, so we provide
	// correct data here.
	lister := &mockZFSLister{
		mounts: []ZFSMountInfo{
			{Name: "tank", Mountpoint: "/mnt/tank"},
		},
	}
	h.SetZFSLister(lister)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?include_zfs=true", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestBrowseExists_EmptyPath checks that an empty path returns 400.
func TestBrowseExists_EmptyPath(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/path-exists", nil)
	w := httptest.NewRecorder()
	h.Exists(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestBrowseExists_ExistingDir checks that an existing directory returns exists=true.
func TestBrowseExists_ExistingDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/path-exists?path="+base, nil)
	w := httptest.NewRecorder()
	h.Exists(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["exists"] != true {
		t.Errorf("exists = %v, want true", resp["exists"])
	}
	if resp["is_dir"] != true {
		t.Errorf("is_dir = %v, want true", resp["is_dir"])
	}
}

// TestBrowseExists_MissingPath checks that a non-existent path returns exists=false.
func TestBrowseExists_MissingPath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	missing := filepath.Join(base, "nonexistent")
	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/path-exists?path="+missing, nil)
	w := httptest.NewRecorder()
	h.Exists(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["exists"] != false {
		t.Errorf("exists = %v, want false", resp["exists"])
	}
}

// TestBrowseExists_OutsideRoots checks that a path outside allowed roots returns exists=false (not 403).
func TestBrowseExists_OutsideRoots(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()

	// /tmp is outside the allowed roots.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/path-exists?path=/tmp/something", nil)
	w := httptest.NewRecorder()
	h.Exists(w, req)

	// Should return 200 with exists=false (not 403) per the handler comment.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["exists"] != false {
		t.Errorf("exists = %v, want false", resp["exists"])
	}
}

// TestBrowseExists_File checks a file (not a dir) returns is_dir=false.
func TestBrowseExists_File(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	filePath := filepath.Join(base, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/path-exists?path="+filePath, nil)
	w := httptest.NewRecorder()
	h.Exists(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["exists"] != true {
		t.Errorf("exists = %v, want true", resp["exists"])
	}
	if resp["is_dir"] != false {
		t.Errorf("is_dir = %v, want false", resp["is_dir"])
	}
}

// TestBrowseList_HiddenEntriesSkipped verifies that hidden directories (dotfiles) are excluded.
func TestBrowseList_HiddenEntriesSkipped(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, ".hidden"), 0o755); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	if err := os.Mkdir(filepath.Join(base, "visible"), 0o755); err != nil {
		t.Fatalf("mkdir visible: %v", err)
	}

	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path="+base, nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries, ok := resp["entries"].([]any)
	if !ok {
		t.Fatalf("entries not a list: %T", resp["entries"])
	}
	// Only the visible directory should appear.
	if len(entries) != 1 {
		t.Errorf("entries len = %d, want 1 (only visible dir); entries: %v", len(entries), entries)
	}
	firstEntry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry not a map: %T", entries[0])
	}
	if firstEntry["name"] != "visible" {
		t.Errorf("entry name = %v, want 'visible'", firstEntry["name"])
	}
}

// TestNormalizePath_AllowedRoot tests that a path under an allowed root is accepted.
func TestNormalizePath_AllowedRoot(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	// /mnt is in browseAllowedRoots.
	got, err := h.normalizePath("/mnt/user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/mnt/user" {
		t.Errorf("got %q, want /mnt/user", got)
	}
}

// TestNormalizePath_Forbidden tests that a path outside allowed roots is rejected.
func TestNormalizePath_Forbidden(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	_, err := h.normalizePath("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside allowed roots, got nil")
	}
}

// TestNormalizePath_ExtraRoots tests that extra ZFS roots work in path validation.
func TestNormalizePath_ExtraRoots(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{"/data/tank"}
	got, err := h.normalizePath("/data/tank/backups")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/tank/backups" {
		t.Errorf("got %q, want /data/tank/backups", got)
	}
}
