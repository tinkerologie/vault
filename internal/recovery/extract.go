package recovery

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// ExtractOptions controls an extraction.
type ExtractOptions struct {
	// Items to extract; empty means every item in the point.
	Items []string
	// Include limits extraction to these item-relative paths (as printed by
	// Contents) and everything beneath them. Empty extracts everything.
	Include []string
	// Dest is the local folder to extract into. Each item gets its own
	// subfolder.
	Dest string
	// Raw copies the stored archives as-is (decrypted and decompressed)
	// instead of unpacking them.
	Raw bool
	// SafeNames rewrites names Windows cannot store. Nil means "only on
	// Windows". It cannot be turned off on Windows, where an unsafe name
	// such as "a:b" would be written as an alternate data stream.
	SafeNames *bool
	// Overwrite allows extracting into an item folder that is not empty.
	Overwrite bool
	// Progress, when set, receives one line per milestone.
	Progress func(string)
}

// Skip is a backed-up path that was deliberately not recreated.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ItemReport summarises the extraction of one item.
type ItemReport struct {
	Item    string   `json:"item"`
	Type    string   `json:"type"`
	Dir     string   `json:"dir"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Pruned  int      `json:"pruned,omitempty"`
	Renamed []Rename `json:"renamed,omitempty"`
	Skipped []Skip   `json:"skipped,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Report summarises an extraction. An item that failed has Error set; the
// other items are still extracted.
type Report struct {
	Point string       `json:"point"`
	Items []ItemReport `json:"items"`
}

// Failed reports whether any item failed.
func (r Report) Failed() bool {
	for _, it := range r.Items {
		if it.Error != "" {
			return true
		}
	}
	return false
}

// Extract recovers items of a restore point into opts.Dest.
func (s *Session) Extract(ctx context.Context, p Point, opts ExtractOptions) (Report, error) {
	if p.Locked {
		return Report{}, p.lockedError()
	}
	if opts.Dest == "" {
		return Report{}, errors.New("a destination folder is required")
	}
	dest, err := filepath.Abs(opts.Dest)
	if err != nil {
		return Report{}, fmt.Errorf("resolve destination: %w", err)
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return Report{}, fmt.Errorf("create destination %s: %w", dest, err)
	}
	safe := defaultSafeNames()
	if opts.SafeNames != nil && !safe {
		safe = *opts.SafeNames
	}
	items, err := selectItems(p, opts.Items)
	if err != nil {
		return Report{}, err
	}

	// Windows-safe names include keeping case twins apart, even on a
	// case-sensitive filesystem: the files may be copied to Windows later.
	rep := Report{Point: p.StoragePath}
	rootNames := newNamer(safe, safe || caseInsensitiveFS())
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		ir := ItemReport{Item: item.Name, Type: item.Type}
		local, err := rootNames.local(item.Name)
		if err == nil && strings.ContainsRune(filepath.ToSlash(local), '/') {
			err = fmt.Errorf("item name %q is not a single folder name", item.Name)
		}
		if err == nil {
			ir.Dir = filepath.Join(dest, local)
			err = s.extractItem(ctx, p, item, ir.Dir, safe, opts, &ir)
		}
		if err != nil {
			ir.Error = err.Error()
		}
		rep.Items = append(rep.Items, ir)
	}
	return rep, nil
}

func selectItems(p Point, names []string) ([]Item, error) {
	if len(names) == 0 {
		return p.Items, nil
	}
	out := make([]Item, 0, len(names))
	for _, n := range names {
		it, ok := p.Item(n)
		if !ok {
			return nil, fmt.Errorf("%s does not contain an item named %q", p.StoragePath, n)
		}
		out = append(out, it)
	}
	return out, nil
}

// extractor writes one item's files under root.
type extractor struct {
	ctx      context.Context
	root     string
	names    *namer
	include  []string
	report   *ItemReport
	progress func(string)

	written  map[string]string // display path -> local file path
	sizes    map[string]int64  // display path -> bytes written
	fromTree map[string]bool   // display paths unpacked from a tree archive
	dirTimes map[string]dirMeta
	checked  map[string]bool // local dirs verified to have no symlink in their path
}

type dirMeta struct {
	mode    os.FileMode
	modTime time.Time
}

func (s *Session) extractItem(ctx context.Context, p Point, item Item, root string, safe bool, opts ExtractOptions, rep *ItemReport) error {
	if err := prepareRoot(root, opts.Overwrite); err != nil {
		return err
	}
	x := &extractor{
		ctx:      ctx,
		root:     root,
		names:    newNamer(safe, safe || caseInsensitiveFS()),
		report:   rep,
		progress: opts.Progress,
		written:  map[string]string{},
		sizes:    map[string]int64{},
		fromTree: map[string]bool{},
		dirTimes: map[string]dirMeta{},
		checked:  map[string]bool{},
	}
	for _, inc := range opts.Include {
		clean, err := cleanBackupPath(inc)
		if err != nil {
			return err
		}
		x.include = append(x.include, clean)
	}
	defer func() { rep.Renamed = x.names.renames }()

	if id, ok := p.manifestID(item.Name); ok {
		if opts.Raw {
			return errors.New("--raw applies to classic backups only; dedup backups have no archives to copy")
		}
		repo, err := s.dedupRepo()
		if err != nil {
			return err
		}
		x.say("extracting %s (%s, deduplicated) to %s", item.Name, item.Type, root)
		if err := s.extractDedup(x, repo, id, item); err != nil {
			return err
		}
	} else {
		chain, err := s.chain(p)
		if err != nil {
			return err
		}
		trees, err := s.treeArchives(p, item)
		if err != nil {
			return err
		}
		if opts.Raw {
			trees = nil
		}
		for i, step := range chain {
			if _, ok := step.Item(item.Name); !ok {
				continue
			}
			if len(chain) > 1 {
				x.say("extracting %s from %s (%d of %d, %s)", item.Name, step.StoragePath, i+1, len(chain), step.BackupType)
			} else {
				x.say("extracting %s (%s) to %s", item.Name, item.Type, root)
			}
			// Files that are not unpacked keep their stored names, so in a
			// chain each step gets its own folder. Otherwise a later step
			// would replace the full backup's data.tar — or, for a VM, its
			// disk image with the incremental delta.
			rawDir := ""
			if (opts.Raw || len(trees) == 0) && len(chain) > 1 {
				rawDir = path.Base(step.StoragePath)
			}
			if err := s.extractClassicStep(x, step, item, trees, opts.Raw, rawDir); err != nil {
				return err
			}
		}
		if len(chain) > 1 && len(trees) == 0 {
			x.say("note: %s is %s; each run's files are in their own folder, oldest first, and must be combined with the original tools (for example qemu-img for VM disks)", item.Name, p.BackupType)
		}
		if len(chain) > 1 && len(trees) > 0 {
			if err := s.pruneDeleted(x, chain[len(chain)-1], item, trees); err != nil {
				return err
			}
		}
	}
	return x.finish()
}

// prepareRoot creates an item's folder, refusing to merge into a non-empty
// one unless overwrite is set.
func prepareRoot(root string, overwrite bool) error {
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a folder", root)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		if len(entries) > 0 && !overwrite {
			return fmt.Errorf("%s is not empty (use --overwrite to extract into it anyway)", root)
		}
		return nil
	}
	return os.MkdirAll(root, 0o750)
}

func (x *extractor) say(format string, args ...any) {
	if x.progress != nil {
		x.progress(fmt.Sprintf(format, args...))
	}
}

func (x *extractor) skip(p, reason string) {
	x.report.Skipped = append(x.report.Skipped, Skip{Path: p, Reason: reason})
}

// wanted applies the --include filter to a display path.
func (x *extractor) wanted(display string) bool {
	if len(x.include) == 0 {
		return true
	}
	for _, inc := range x.include {
		if inc == "" || display == inc || strings.HasPrefix(display, inc+"/") {
			return true
		}
	}
	return false
}

// target maps a display path to a local path, ensuring every existing
// component between root and the path is a real directory (never a symlink),
// so a pre-existing link in an --overwrite target cannot redirect writes.
func (x *extractor) target(display string) (string, error) {
	rel, err := x.names.local(display)
	if err != nil {
		return "", err
	}
	// The namer already confines rel; this is the explicit barrier (CWE-22)
	// static analysis recognises, and a second line of defence.
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%q is not a path inside the item folder", display)
	}
	full := filepath.Join(x.root, rel)
	if !isWithin(x.root, full) {
		return "", fmt.Errorf("%q resolves outside the item folder", display)
	}
	if err := x.checkParents(filepath.Dir(full)); err != nil {
		return "", err
	}
	return full, nil
}

func (x *extractor) checkParents(dir string) error {
	if x.checked[dir] || dir == x.root || !isWithin(x.root, dir) {
		return nil
	}
	if err := x.checkParents(filepath.Dir(dir)); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("refusing to write through symlink %s", dir)
	case !info.IsDir():
		return fmt.Errorf("%s exists and is not a folder", dir)
	}
	x.checked[dir] = true
	return nil
}

func isWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (x *extractor) mkdir(display string, mode os.FileMode, modTime time.Time) error {
	full, err := x.target(display)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(full, 0o750); err != nil {
		return err
	}
	x.dirTimes[full] = dirMeta{mode: mode, modTime: modTime}
	return nil
}

// writeFile creates display with the bytes from r.
func (x *extractor) writeFile(display string, r io.Reader, mode os.FileMode, modTime time.Time) (int64, error) {
	full, err := x.target(display)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return 0, err
	}
	if info, err := os.Lstat(full); err == nil && !info.Mode().IsRegular() {
		return 0, fmt.Errorf("refusing to replace %s, which is not a regular file", full)
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- full is confined to the item folder by target()
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, &ctxReader{ctx: x.ctx, r: r})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("write %s: %w", full, err)
	}
	applyFileMeta(full, mode, modTime)
	x.written[display] = full
	x.sizes[display] = n
	return n, nil
}

// applyFileMeta restores permission bits (not on Windows, where they would
// only make recovered files read-only) and the modification time.
func applyFileMeta(full string, mode os.FileMode, modTime time.Time) {
	if runtime.GOOS != "windows" && mode != 0 {
		_ = os.Chmod(full, mode.Perm()|0o200)
	}
	if !modTime.IsZero() {
		_ = os.Chtimes(full, modTime, modTime)
	}
}

// finish totals the files left in place (a later chain step may rewrite or
// prune a file) and applies directory metadata deepest first, once nothing
// else needs to be written inside them.
func (x *extractor) finish() error {
	x.report.Files = len(x.written)
	x.report.Bytes = 0
	for _, n := range x.sizes {
		x.report.Bytes += n
	}
	dirs := make([]string, 0, len(x.dirTimes))
	for d := range x.dirTimes {
		dirs = append(dirs, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		applyFileMeta(d, x.dirTimes[d].mode|0o700, x.dirTimes[d].modTime)
	}
	return x.ctx.Err()
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// extractDedup rebuilds a dedup item's files from their chunks.
func (s *Session) extractDedup(x *extractor, repo *dedup.Repo, id dedup.ID, item Item) error {
	m, err := repo.GetManifest(id)
	if err != nil {
		return fmt.Errorf("read dedup manifest: %w", err)
	}
	return walkDedup(repo, item.Type, m, func(display string, e dedup.ManifestEntry) error {
		if display == "" || !x.wanted(display) {
			return nil
		}
		// Same rule as the classic path: one name that cannot be mapped
		// (escaping, or not valid UTF-8) is reported, not fatal for the item.
		if _, err := x.names.local(display); err != nil {
			x.skip(display, err.Error())
			return nil
		}
		if e.IsDir {
			return x.mkdir(display, os.FileMode(e.Mode).Perm(), parseTime(e.ModTime))
		}
		n, err := x.writeFile(display, &chunkReader{repo: repo, chunks: e.Chunks}, os.FileMode(e.Mode).Perm(), parseTime(e.ModTime))
		if err != nil {
			return err
		}
		if n != e.Size {
			return fmt.Errorf("%s: rebuilt %d bytes, backup recorded %d", display, n, e.Size)
		}
		return nil
	})
}

// chunkReader streams a file's chunks in order.
type chunkReader struct {
	repo   *dedup.Repo
	chunks []dedup.ID
	buf    []byte
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if len(c.chunks) == 0 {
			return 0, io.EOF
		}
		data, err := c.repo.Get(c.chunks[0])
		if err != nil {
			return 0, fmt.Errorf("read chunk %s: %w", c.chunks[0], err)
		}
		c.buf, c.chunks = data, c.chunks[1:]
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// extractClassicStep unpacks one restore point's stored files for an item.
// Tree archives are streamed straight from storage into the item folder;
// other files are copied as-is.
func (s *Session) extractClassicStep(x *extractor, step Point, item Item, trees map[string]treeArchive, raw bool, rawDir string) error {
	files, err := s.itemFiles(step, item.Name)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		x.skip(item.Name, "no data stored for this item in "+step.StoragePath)
		return nil
	}
	sums := step.checksums[item.Name]
	for _, f := range files {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if !raw && isSidecar(f.Path) {
			continue
		}
		if err := s.extractStoredFile(x, f, sums[path.Base(f.Path)], trees, rawDir); err != nil {
			return fmt.Errorf("%s: %w", path.Base(f.Path), err)
		}
	}
	return nil
}

func (s *Session) extractStoredFile(x *extractor, f storage.FileInfo, wantSum string, trees map[string]treeArchive, rawDir string) error {
	rc, err := s.adapter.Read(f.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	hasher := sha256.New()
	stored := io.TeeReader(rc, hasher)
	plain, closeFn, localName, err := runner.OpenStoredStream(stored, path.Base(f.Path), s.passphrase)
	if err != nil {
		return err
	}
	defer closeFn() //nolint:errcheck // read-only stream

	if tree, ok := trees[archiveKey(localName)]; ok {
		if err := x.untar(plain, tree); err != nil {
			return err
		}
	} else {
		display := localName
		switch {
		case rawDir != "":
			display = path.Join(rawDir, localName)
		case len(trees) > 0:
			display = path.Join(metadataDir, localName)
		}
		if x.wanted(display) {
			if _, err := x.writeFile(display, plain, 0o644, f.ModTime); err != nil {
				return err
			}
		}
	}
	// Drain what the reader did not need (tar padding, a skipped file) so
	// the checksum covers the whole stored object.
	if _, err := io.Copy(io.Discard, stored); err != nil {
		return err
	}
	if wantSum != "" {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSum {
			return fmt.Errorf("checksum mismatch: stored object is %s, backup recorded %s", got, wantSum)
		}
	}
	return nil
}

// untar extracts a classic tar archive under its tree prefix.
func (x *extractor) untar(r io.Reader, tree treeArchive) error {
	plain, closeFn, err := engine.DecompressingReader(r)
	if err != nil {
		return err
	}
	defer closeFn() //nolint:errcheck // read-only stream
	tr := tar.NewReader(plain)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		rel, err := cleanBackupPath(hdr.Name)
		if err != nil {
			x.skip(hdr.Name, err.Error())
			continue
		}
		display := path.Join(tree.prefix, rel)
		if tree.isFile {
			display = tree.prefix
		}
		if display == "" || !x.wanted(display) {
			continue
		}
		mode := os.FileMode(hdr.Mode).Perm() //nolint:gosec // tar modes are 12-bit
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := x.mkdir(display, mode, hdr.ModTime); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA appears in archives written by old tar versions
			if _, err := x.writeFile(display, tr, mode, hdr.ModTime); err != nil {
				return err
			}
			x.fromTree[display] = true
		case tar.TypeLink:
			linkRel, err := cleanBackupPath(hdr.Linkname)
			if err == nil && !filepath.IsLocal(filepath.FromSlash(linkRel)) {
				err = fmt.Errorf("target %q is outside the item", hdr.Linkname)
			}
			if err != nil {
				x.skip(display, "hard link: "+err.Error())
				continue
			}
			if err := x.copyHardlink(display, path.Join(tree.prefix, linkRel), mode, hdr.ModTime); err != nil {
				x.skip(display, "hard link: "+err.Error())
			} else {
				x.fromTree[display] = true
			}
		case tar.TypeSymlink:
			if reason := x.symlink(display, hdr.Linkname); reason != "" {
				x.skip(display, "symbolic link to "+hdr.Linkname+": "+reason)
			}
		default:
			x.skip(display, fmt.Sprintf("special file (tar type %q) not recreated", hdr.Typeflag))
		}
	}
}

// symlink recreates a relative symbolic link that stays inside the item
// folder, returning why it did not when it cannot. Windows needs elevated
// rights to create links, and an absolute or escaping link would point at the
// recovering machine's own files, so those are reported instead.
func (x *extractor) symlink(display, linkname string) string {
	if runtime.GOOS == "windows" {
		return "not recreated on Windows"
	}
	if linkname == "" || path.IsAbs(linkname) || filepath.IsAbs(linkname) {
		return "absolute links are not recreated"
	}
	// ".." is only allowed as a leading run ("../../x"). After a normal
	// segment ("a/../.."), the lexical check below and the real resolution
	// can disagree once "a" is itself a link extracted earlier.
	seenName := false
	for _, seg := range strings.Split(linkname, "/") {
		switch {
		case seg == "..":
			if seenName {
				return `".." after a folder name is not recreated`
			}
		case seg != "" && seg != ".":
			seenName = true
		}
	}
	// Where the link points, relative to the item root.
	resolved := filepath.FromSlash(path.Join(path.Dir(display), linkname))
	if !filepath.IsLocal(resolved) {
		return "points outside the item, not recreated"
	}
	full, err := x.target(display)
	if err != nil {
		return err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err.Error()
	}
	// The link is created in a real directory inside the item: resolve any
	// links already on disk so an earlier entry cannot redirect this one.
	realRoot, err := filepath.EvalSymlinks(x.root)
	if err != nil {
		return err.Error()
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil || !isWithin(realRoot, realDir) {
		return "its folder resolves outside the item, not recreated"
	}
	// A target that already exists may itself be reached through links
	// extracted earlier: it has to resolve inside the item as well.
	if realTarget, err := filepath.EvalSymlinks(filepath.Join(realDir, filepath.FromSlash(linkname))); err == nil && !isWithin(realRoot, realTarget) {
		return "resolves outside the item, not recreated"
	}
	if info, err := os.Lstat(full); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "a file already exists at this path"
		}
		if err := os.Remove(full); err != nil {
			return err.Error()
		}
	}
	if err := os.Symlink(filepath.FromSlash(linkname), filepath.Join(realDir, filepath.Base(full))); err != nil {
		return err.Error()
	}
	return ""
}

// copyHardlink recreates a hard link as an independent copy of the file it
// points at, which must already have been extracted.
func (x *extractor) copyHardlink(display, linkTarget string, mode os.FileMode, modTime time.Time) error {
	src, ok := x.written[linkTarget]
	if !ok {
		return fmt.Errorf("target %s was not extracted", linkTarget)
	}
	f, err := os.Open(src) // #nosec G304 -- src was written by this extraction
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = x.writeFile(display, f, mode, modTime)
	return err
}

// pruneDeleted removes files an earlier chain step restored that were deleted
// before the newest step, using the newest step's effective listing — the
// same rule a daemon restore applies. Without a listing nothing is removed.
func (s *Session) pruneDeleted(x *extractor, newest Point, item Item, trees map[string]treeArchive) error {
	files, err := s.itemFiles(newest, item.Name)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	covered := map[string]treeArchive{}
	for _, f := range files {
		if !isSidecar(f.Path) || !strings.HasSuffix(strings.TrimSuffix(f.Path, ".age"), engine.ListingSuffix) {
			continue
		}
		tree, ok := trees[archiveKey(f.Path)]
		if !ok {
			continue
		}
		var listing engine.TarIndex
		if err := s.readJSON(f.Path, &listing); err != nil {
			return fmt.Errorf("read listing %s: %w", f.Path, err)
		}
		covered[archiveKey(f.Path)] = tree
		for _, e := range listing.Files {
			if rel, err := cleanBackupPath(e.Path); err == nil {
				if tree.isFile {
					keep[tree.prefix] = true
				} else {
					keep[path.Join(tree.prefix, rel)] = true
				}
			}
		}
	}
	if len(covered) == 0 {
		x.say("note: %s has no file listing, so files deleted between backups may reappear", newest.StoragePath)
		return nil
	}
	for display, full := range x.written {
		// Only files unpacked from a tree archive can be stale; metadata
		// files are rewritten from the newest step on every replay.
		if keep[display] || !x.fromTree[display] || !underAnyTree(display, covered) {
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(x.written, display)
		delete(x.sizes, display)
		x.report.Pruned++
	}
	return nil
}

func underAnyTree(display string, trees map[string]treeArchive) bool {
	for _, t := range trees {
		if t.prefix == "" || display == t.prefix || strings.HasPrefix(display, t.prefix+"/") {
			return true
		}
	}
	return false
}
