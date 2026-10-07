package recovery

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/runner"
)

// Item is one backed-up item (folder, container, VM, …) in a restore point.
type Item struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Point is one backup run found on storage.
type Point struct {
	Job         string    `json:"job"`
	StoragePath string    `json:"storage_path"`
	CreatedAt   time.Time `json:"created_at"`
	BackupType  string    `json:"backup_type"`
	// Encryption is the job's encryption setting ("none" or "age").
	Encryption string `json:"encryption,omitempty"`
	// Locked is true when the run's manifest is encrypted and could not be
	// opened with the key or passphrase supplied. LockedBy says which secret
	// it needs: "dedup" (the server key) or "age" (the backup passphrase).
	Locked   bool   `json:"locked,omitempty"`
	LockedBy string `json:"locked_by,omitempty"`
	Items    []Item `json:"items,omitempty"`

	itemManifests map[string]string
	checksums     map[string]map[string]string
}

// IsDedup reports whether the point's items are stored as dedup manifests.
func (p Point) IsDedup() bool { return len(p.itemManifests) > 0 }

// Item returns the named item, or false when the point does not contain it.
func (p Point) Item(name string) (Item, bool) {
	for _, it := range p.Items {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

func (p Point) manifestID(item string) (dedup.ID, bool) {
	raw, ok := p.itemManifests[item]
	if !ok {
		return dedup.ID{}, false
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != len(dedup.ID{}) {
		return dedup.ID{}, false
	}
	var id dedup.ID
	copy(id[:], decoded)
	return id, true
}

// Points lists every backup run on the destination, oldest first within each
// job. The scan reads each run's manifest.json, so it works with no database.
func (s *Session) Points() ([]Point, error) {
	if s.points != nil {
		return s.points, nil
	}
	raw, err := runner.ScanManifests(s.adapter, s.db, s.destID, s.serverKey, s.passphrase)
	if err != nil {
		return nil, fmt.Errorf("scan storage for backups: %w", err)
	}
	points := make([]Point, 0, len(raw))
	for _, m := range raw {
		points = append(points, pointFromManifest(m))
	}
	sort.SliceStable(points, func(i, j int) bool {
		a, b := points[i], points[j]
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.StoragePath < b.StoragePath
	})
	s.points = points
	return points, nil
}

func pointFromManifest(m map[string]any) Point {
	p := Point{
		Job:         str(m["job_name"]),
		StoragePath: filepath.ToSlash(str(m["storage_path"])),
		BackupType:  str(m["backup_type"]),
		Encryption:  str(m["encryption"]),
		Locked:      m["encrypted"] == true,
		LockedBy:    str(m["key"]),
	}
	if p.Job == "" {
		p.Job = path.Dir(p.StoragePath)
	}
	if t, err := time.Parse(time.RFC3339, str(m["created_at"])); err == nil {
		p.CreatedAt = t
	} else if t, err := time.Parse("2006-01-02_150405", str(m["timestamp"])); err == nil {
		p.CreatedAt = t
	} else if t, ok := runFolderTime(p.StoragePath); ok {
		// A locked (encrypted) manifest carries no readable dates; the run
		// folder's name still does, so locked runs sort in time order too.
		p.CreatedAt = t
	}
	if items, ok := m["items"].([]any); ok {
		for _, raw := range items {
			if it, ok := raw.(map[string]any); ok && str(it["name"]) != "" {
				p.Items = append(p.Items, Item{Name: str(it["name"]), Type: str(it["type"])})
			}
		}
	}
	if im, ok := m["item_manifests"].(map[string]any); ok {
		p.itemManifests = make(map[string]string, len(im))
		for k, v := range im {
			if s := str(v); s != "" {
				p.itemManifests[k] = s
			}
		}
	}
	if cs, ok := m["checksums"].(map[string]any); ok {
		p.checksums = make(map[string]map[string]string, len(cs))
		for item, files := range cs {
			fm, ok := files.(map[string]any)
			if !ok {
				continue
			}
			p.checksums[item] = make(map[string]string, len(fm))
			for f, sum := range fm {
				p.checksums[item][f] = str(sum)
			}
		}
	}
	return p
}

// runFolderTime parses the time from a run folder name: "2006-01-02_150405",
// or "<run id>_2006-01-02_150405" for backups made before issue #319.
// The name is in server-local time, which is close enough for ordering.
func runFolderTime(storagePath string) (time.Time, bool) {
	const layout = "2006-01-02_150405"
	base := path.Base(storagePath)
	if len(base) < len(layout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(layout, base[len(base)-len(layout):], time.Local)
	return t, err == nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// lockedError explains what is needed to open a locked point.
func (p Point) lockedError() error {
	switch p.LockedBy {
	case "dedup":
		return fmt.Errorf("%s is a deduplicated backup: pass --key with the vault.key from the server that made it, or the backup passphrase that was set when it ran", p.StoragePath)
	case "age":
		return fmt.Errorf("%s is encrypted: the backup passphrase is missing or wrong (--passphrase-file or $VAULT_PASSPHRASE)", p.StoragePath)
	default:
		return fmt.Errorf("%s has an encrypted manifest that could not be opened", p.StoragePath)
	}
}

// FindPoint resolves a point reference as printed by List: either a storage
// path ("<job>/<run>") or "<job>/latest".
func (s *Session) FindPoint(ref string) (Point, error) {
	points, err := s.Points()
	if err != nil {
		return Point{}, err
	}
	ref = strings.Trim(filepath.ToSlash(ref), "/")
	if job, ok := strings.CutSuffix(ref, "/latest"); ok {
		// Points are in time order, so the job's last entry is its newest
		// run. When that one is locked, say so instead of quietly falling
		// back to an older backup the user did not ask for.
		var newest *Point
		for i := range points {
			if points[i].Job == job {
				newest = &points[i]
			}
		}
		switch {
		case newest == nil:
			return Point{}, fmt.Errorf("no backups for job %q (run `vault recover list` to see what is available)", job)
		case newest.Locked:
			return Point{}, newest.lockedError()
		default:
			return *newest, nil
		}
	}
	for _, p := range points {
		if p.StoragePath == ref {
			if p.Locked {
				return Point{}, p.lockedError()
			}
			return p, nil
		}
	}
	return Point{}, fmt.Errorf("no backup at %q (run `vault recover list` to see what is available)", ref)
}

// chain returns the restore points to replay, oldest first, to rebuild a
// classic item of p: the last full backup before it plus, for an incremental,
// every increment in between. Manifests record no parent pointer, so the
// chain is inferred from the job's run order. Callers use it only for items
// without a dedup manifest (dedup items are self-contained), which is why it
// does not look at p.IsDedup: a dedup destination can still hold classic
// items such as VMs.
func (s *Session) chain(p Point) ([]Point, error) {
	if p.BackupType != "incremental" && p.BackupType != "differential" {
		return []Point{p}, nil
	}
	points, err := s.Points()
	if err != nil {
		return nil, err
	}
	var job []Point
	target := -1
	for _, q := range points {
		if q.Job != p.Job || q.Locked {
			continue
		}
		if q.StoragePath == p.StoragePath {
			target = len(job)
		}
		job = append(job, q)
	}
	if target < 0 {
		return nil, fmt.Errorf("restore point %s not found in job %q", p.StoragePath, p.Job)
	}
	base := -1
	for i := target - 1; i >= 0; i-- {
		if job[i].BackupType == "full" || job[i].BackupType == "" {
			base = i
			break
		}
	}
	if base < 0 {
		return nil, fmt.Errorf("%s is %s but no earlier full backup of job %q is on this storage", p.StoragePath, p.BackupType, p.Job)
	}
	if p.BackupType == "differential" {
		return []Point{job[base], p}, nil
	}
	return append([]Point(nil), job[base:target+1]...), nil
}

// readJSON reads one storage object, undoing encryption and transport
// compression, and decodes it into v.
func (s *Session) readJSON(name string, v any) error {
	rc, err := s.adapter.Read(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	plain, closeFn, _, err := runner.OpenStoredStream(rc, path.Base(name), s.passphrase)
	if err != nil {
		return err
	}
	defer closeFn() //nolint:errcheck // read-only stream
	return json.NewDecoder(plain).Decode(v)
}
