package recovery

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// Entry is one path in an item's backup, relative to the item root and
// '/'-separated. Container volume files are prefixed with the volume's path
// inside the container, without the leading slash ("config/app.yml").
type Entry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	ModTime string `json:"modtime,omitempty"`
}

// Contents lists the files of one item in a restore point. For a classic
// incremental or differential point it is the full set of files the point
// restores, not just what changed in that run.
func (s *Session) Contents(p Point, itemName string) ([]Entry, error) {
	item, ok := p.Item(itemName)
	if !ok {
		return nil, fmt.Errorf("%s does not contain an item named %q", p.StoragePath, itemName)
	}
	var out []Entry
	if id, ok := p.manifestID(item.Name); ok {
		repo, err := s.dedupRepo()
		if err != nil {
			return nil, err
		}
		m, err := repo.GetManifest(id)
		if err != nil {
			return nil, fmt.Errorf("read dedup manifest for %s: %w", item.Name, err)
		}
		err = walkDedup(repo, item.Type, m, func(p string, e dedup.ManifestEntry) error {
			out = append(out, Entry{Path: p, Size: e.Size, IsDir: e.IsDir, Mode: e.Mode & 0o7777, ModTime: e.ModTime})
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		if out, err = s.classicContents(p, item); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// walkDedup visits every restorable path of a dedup item. Folder and plugin
// manifests map paths to entries directly. A container manifest holds
// synthetic metadata keys (skipped) and one pointer per volume whose files
// live in a sub-manifest; those are expanded under the volume's path.
func walkDedup(repo *dedup.Repo, itemType string, m dedup.Manifest, fn func(string, dedup.ManifestEntry) error) error {
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := m.Files[k]
		if itemType != "container" {
			if err := fn(strings.TrimPrefix(k, "/"), e); err != nil {
				return err
			}
			continue
		}
		if engine.IsSyntheticContainerKey(k) {
			// Container metadata is worth having on a new host (the Unraid
			// template recreates the container); markers without data are not.
			if name, ok := containerMetadataNames[k]; ok && len(e.Chunks) > 0 {
				if err := fn(path.Join(metadataDir, name), e); err != nil {
					return err
				}
			}
			continue
		}
		if dest, ok := engine.ContainerVolumeFileDest(k); ok {
			if err := fn(strings.TrimPrefix(dest, "/"), e); err != nil {
				return err
			}
			continue
		}
		dest, ok := engine.ContainerVolumeDest(k)
		if !ok {
			if err := fn(strings.TrimPrefix(k, "/"), e); err != nil {
				return err
			}
			continue
		}
		if engine.IsSkippedVolumeEntry(e) || len(e.Chunks) == 0 {
			continue
		}
		sub, err := repo.GetManifest(e.Chunks[0])
		if err != nil {
			return fmt.Errorf("read volume %s: %w", dest, err)
		}
		root := strings.TrimPrefix(dest, "/")
		if err := fn(root, dedup.ManifestEntry{IsDir: true, Mode: e.Mode, ModTime: e.ModTime}); err != nil {
			return err
		}
		subKeys := make([]string, 0, len(sub.Files))
		for sk := range sub.Files {
			subKeys = append(subKeys, sk)
		}
		sort.Strings(subKeys)
		for _, sk := range subKeys {
			if err := fn(path.Join(root, sk), sub.Files[sk]); err != nil {
				return err
			}
		}
	}
	return nil
}

// containerMetadataNames maps a dedup container's synthetic metadata keys to
// the file names they are recovered under, inside metadataDir.
var containerMetadataNames = map[string]string{
	engine.ContainerInspectKey:   "inspect.json",
	engine.ContainerImageMetaKey: "image_meta.json",
	engine.ContainerTemplateKey:  "template.xml",
	engine.ContainerDBDumpKey:    "database-dump",
}

// treeArchive says where a classic archive's contents belong in the item.
type treeArchive struct {
	prefix string // item-relative directory (or file, when isFile) to unpack into
	isFile bool   // single-file bind mount: the archive holds one file
}

// archiveKey reduces a stored archive or sidecar name to the archive's base
// name ("data.tar.zst.age" -> "data.tar"), so names from listings, indexes,
// volumes.json and storage all compare equal.
func archiveKey(name string) string {
	base := path.Base(name)
	if i := strings.Index(base, ".tar"); i >= 0 {
		return base[:i+len(".tar")]
	}
	return base
}

// isSidecar reports whether a stored file is a tar index or listing sidecar.
func isSidecar(name string) bool {
	base := strings.TrimSuffix(path.Base(name), ".age")
	return strings.Contains(base, ".tar") &&
		(strings.HasSuffix(base, engine.IndexSuffix) || strings.HasSuffix(base, engine.ListingSuffix))
}

// treeArchives returns the classic archives of an item that hold a file tree,
// keyed by archiveKey. Other files (container images, VM disks, metadata) are
// recovered as-is. A container's volume map comes from its volumes.json; when
// that file exists but cannot be read (or decrypted) it is an error, rather
// than silently recovering the volume archives as opaque files.
func (s *Session) treeArchives(p Point, item Item) (map[string]treeArchive, error) {
	switch item.Type {
	case "folder":
		return map[string]treeArchive{"data.tar": {}}, nil
	case "plugin":
		return map[string]treeArchive{"config.tar": {}}, nil
	case "container":
		var vols []struct {
			Destination string `json:"destination"`
			BackedUp    bool   `json:"backed_up"`
			Archive     string `json:"archive"`
			IsFile      bool   `json:"is_file"`
		}
		out := map[string]treeArchive{}
		found := false
		for _, name := range []string{"volumes.json", "volumes.json.age"} {
			err := s.readJSON(path.Join(p.StoragePath, item.Name, name), &vols)
			if err == nil {
				found = true
				break
			}
			if !storage.IsNotExist(err) {
				return nil, fmt.Errorf("read %s volume map %s: %w", item.Name, name, err)
			}
		}
		if !found {
			// No volume map recorded: the volume archives are recovered
			// as stored files rather than unpacked.
			return out, nil
		}
		for _, v := range vols {
			if v.BackedUp && v.Archive != "" {
				out[archiveKey(v.Archive)] = treeArchive{prefix: strings.TrimPrefix(v.Destination, "/"), isFile: v.IsFile}
			}
		}
		return out, nil
	default:
		return nil, nil
	}
}

// itemFiles lists the stored objects of one item in one restore point.
func (s *Session) itemFiles(p Point, item string) ([]storage.FileInfo, error) {
	files, err := s.adapter.List(path.Join(p.StoragePath, item))
	if err != nil {
		if storage.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := files[:0]
	for _, f := range files {
		if !f.IsDir {
			// Adapters built on filepath return OS separators; recovery
			// works in storage paths, which are always '/'-separated.
			f.Path = filepath.ToSlash(f.Path)
			out = append(out, f)
		}
	}
	return out, nil
}

// classicContents builds a classic item's file list from its sidecars: the
// newest chain step's effective listings when present (they describe the
// whole tree at that point), otherwise the union of every step's tar index.
func (s *Session) classicContents(p Point, item Item) ([]Entry, error) {
	chain, err := s.chain(p)
	if err != nil {
		return nil, err
	}
	trees, err := s.treeArchives(p, item)
	if err != nil {
		return nil, err
	}
	byPath := map[string]Entry{}
	add := func(sidecar string, idx engine.TarIndex) {
		tree, ok := trees[archiveKey(sidecar)]
		if !ok {
			return
		}
		for _, f := range idx.Files {
			rel, err := cleanBackupPath(f.Path)
			if err != nil {
				continue
			}
			full := path.Join(tree.prefix, rel)
			if tree.isFile {
				full = tree.prefix
			}
			byPath[full] = Entry{Path: full, Size: f.Size, IsDir: f.IsDir, Mode: parseMode(f.Mode), ModTime: f.ModTime}
		}
	}
	readSidecars := func(step Point, suffix string) (bool, error) {
		files, err := s.itemFiles(step, item.Name)
		if err != nil {
			return false, err
		}
		found := false
		for _, f := range files {
			if !strings.HasSuffix(strings.TrimSuffix(f.Path, ".age"), suffix) || !isSidecar(f.Path) {
				continue
			}
			var idx engine.TarIndex
			if err := s.readJSON(f.Path, &idx); err != nil {
				return false, fmt.Errorf("read %s: %w", f.Path, err)
			}
			add(f.Path, idx)
			found = true
		}
		return found, nil
	}

	newest := chain[len(chain)-1]
	found, err := readSidecars(newest, engine.ListingSuffix)
	if err != nil {
		return nil, err
	}
	if !found {
		for _, step := range chain {
			if _, ok := step.Item(item.Name); !ok {
				continue
			}
			if _, err := readSidecars(step, engine.IndexSuffix); err != nil {
				return nil, err
			}
		}
	}
	if len(byPath) == 0 && len(trees) > 0 {
		return nil, errors.New("this backup has no file index; use `vault recover extract` to unpack it")
	}
	// Files that are not tree archives (VM disks, container images, metadata)
	// are listed as stored, under their recovered name.
	files, err := s.itemFiles(newest, item.Name)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if isSidecar(f.Path) {
			continue
		}
		if _, isTree := trees[archiveKey(f.Path)]; isTree {
			continue
		}
		name := rawName(f.Path)
		dir := ""
		if len(trees) > 0 {
			dir = metadataDir
		}
		full := path.Join(dir, name)
		byPath[full] = Entry{Path: full, Size: f.Size}
	}
	out := make([]Entry, 0, len(byPath))
	for _, e := range byPath {
		out = append(out, e)
	}
	return out, nil
}

// metadataDir holds an item's non-tree files (container config, image,
// templates) when the item also has a file tree, so they never collide with
// the user's own files.
const metadataDir = "_vault-metadata"

// rawName is the local name a stored object is recovered under: the name
// OpenStoredStream reports once ".age" and one compression suffix are
// removed (engine-written objects only carry those suffixes when the content
// really is encrypted or compressed).
func rawName(stored string) string {
	name := strings.TrimSuffix(path.Base(stored), ".age")
	if trimmed, ok := strings.CutSuffix(name, ".gz"); ok {
		return trimmed
	}
	return strings.TrimSuffix(name, ".zst")
}

func parseMode(s string) uint32 {
	var m uint32
	if _, err := fmt.Sscanf(s, "%o", &m); err != nil {
		return 0
	}
	return m
}
