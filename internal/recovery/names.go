package recovery

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Rename records one backed-up path that had to be written under a different
// local name, so the user can find it again.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// namer maps item-relative backup paths ('/'-separated, from Linux) onto
// local relative paths. Every result stays inside the extraction root.
//
// With safe set it also rewrites names Windows cannot store — reserved
// characters, trailing dots and spaces, device names such as CON — and keeps
// names that differ only by case apart, because NTFS and the default macOS
// volume would otherwise merge them silently. Mapping is per source path, so
// a renamed directory keeps every file beneath it together.
type namer struct {
	safe    bool
	fold    bool
	byPath  map[string]string // source path -> local path
	claimed map[string]string // folded local path -> source path that owns it
	renames []Rename
}

func newNamer(safe, fold bool) *namer {
	return &namer{
		safe:    safe,
		fold:    fold,
		byPath:  map[string]string{"": ""},
		claimed: map[string]string{"": ""},
	}
}

// local returns the local relative path (using the OS separator) for a backup
// path. It rejects absolute paths and anything that would climb out of the
// extraction root; a leading "./" or "/" is tolerated because tar archives
// and container volume paths use both.
func (n *namer) local(src string) (string, error) {
	clean, err := cleanBackupPath(src)
	if err != nil {
		return "", err
	}
	if got, ok := n.byPath[clean]; ok {
		return filepath.FromSlash(got), nil
	}
	parentSrc, base := path.Split(clean)
	parentSrc = strings.TrimSuffix(parentSrc, "/")
	parentLocal, err := n.local(parentSrc)
	if err != nil {
		return "", err
	}
	parentLocal = filepath.ToSlash(parentLocal)

	name := base
	if n.safe {
		name = windowsSafeName(base)
	}
	candidate := path.Join(parentLocal, name)
	for i := 2; ; i++ {
		owner, taken := n.claimed[n.key(candidate)]
		if !taken || owner == clean {
			break
		}
		candidate = path.Join(parentLocal, numberedName(name, i))
	}
	n.byPath[clean] = candidate
	n.claimed[n.key(candidate)] = clean
	if path.Base(candidate) != base {
		n.renames = append(n.renames, Rename{From: clean, To: candidate})
	}
	return filepath.FromSlash(candidate), nil
}

func (n *namer) key(p string) string {
	if n.fold {
		return strings.ToLower(p)
	}
	return p
}

// cleanBackupPath normalises an untrusted backup path to a clean relative
// '/'-separated form, or fails when it would escape the extraction root.
func cleanBackupPath(src string) (string, error) {
	p := strings.TrimLeft(src, "/")
	if p == "" || p == "." || p == "./" {
		return "", nil
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes the extraction folder", src)
	}
	if !utf8.ValidString(clean) {
		return "", fmt.Errorf("path %q is not valid UTF-8", src)
	}
	return clean, nil
}

// windowsReserved are device names Windows refuses as a file or directory
// name, with or without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
}

func init() {
	for _, prefix := range []string{"COM", "LPT"} {
		for _, d := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
			windowsReserved[prefix+d] = true
		}
	}
}

// windowsSafeName rewrites one path component so Windows can store it:
// characters it reserves (and control characters) become '_', trailing dots
// and spaces become '_', and device names get a leading '_'.
func windowsSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	trimmed := strings.TrimRight(out, ". ")
	if trimmed != out {
		out = trimmed + strings.Repeat("_", len(out)-len(trimmed))
	}
	stem, _, _ := strings.Cut(out, ".")
	if windowsReserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		out = "_" + out
	}
	return out
}

// numberedName turns "report.txt" into "report (2).txt" for a collision.
func numberedName(name string, i int) string {
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}
