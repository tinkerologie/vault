package engine

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// maxExtractSize is the maximum size for a single file extracted from a tar
// archive, used to prevent decompression bombs. 50 GiB accommodates container
// images and volume data.
const maxExtractSize = 50 << 30 // 50 GiB

// safeFileMode converts a tar header mode (int64) to os.FileMode, clamping to
// the valid permission‐bit range to prevent integer overflow (gosec G115).
func safeFileMode(mode int64) os.FileMode {
	if mode < 0 || mode > math.MaxUint32 {
		return 0o644
	}
	return os.FileMode(mode) & 0o7777
}

// skipPrefixes are Unraid paths that contain large shared data (media,
// downloads, ISOs, etc.) and must NOT be backed up as container volumes.
// Only appdata directories contain the actual container configuration.
var skipPrefixes = []string{
	"/mnt/user/media",
	"/mnt/user/downloads",
	"/mnt/user/data",
	"/mnt/user/isos",
	"/mnt/user/domains",
	"/mnt/user/backups",
	"/mnt/user/system",
	"/mnt/remotes",
}

// appdataPrefixes are paths that always contain container config data.
var appdataPrefixes = []string{
	"/mnt/cache/appdata",
	"/mnt/user/appdata",
}

// volumeManifestEntry describes a single bind mount for the volumes.json manifest.
type volumeManifestEntry struct {
	Index         int      `json:"index"`
	Source        string   `json:"source"`
	Destination   string   `json:"destination"`
	BackedUp      bool     `json:"backed_up"`
	SkipReason    string   `json:"skip_reason,omitempty"`
	Archive       string   `json:"archive,omitempty"`
	IsFile        bool     `json:"is_file,omitempty"`
	ExcludedPaths []string `json:"excluded_paths,omitempty"`
}

// devicePrefixes are virtual / system filesystem paths that must never be
// backed up — they contain device nodes and kernel interfaces, not data.
var devicePrefixes = []string{
	"/dev",
	"/proc",
	"/sys",
	"/run",
}

// shouldSkipVolume returns (skip bool, reason string) for an Unraid bind mount.
// It skips known shared data paths and large non-appdata volumes.
func shouldSkipVolume(source string) (bool, string) {
	norm := filepath.Clean(source)

	// Skip device and virtual filesystem paths.
	for _, prefix := range devicePrefixes {
		if norm == prefix || strings.HasPrefix(norm, prefix+"/") {
			return true, fmt.Sprintf("device/virtual path (%s)", prefix)
		}
	}

	// Always back up appdata volumes.
	for _, prefix := range appdataPrefixes {
		if strings.HasPrefix(norm, prefix) {
			return false, ""
		}
	}

	// Always back up /boot paths (Unraid flash drive configs).
	if strings.HasPrefix(norm, "/boot") {
		return false, ""
	}

	// Skip known shared data paths.
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(norm, prefix) {
			return true, fmt.Sprintf("shared data volume (%s)", prefix)
		}
	}

	// Skip direct array-disk access paths (/mnt/disk1, /mnt/disk2, etc.). A
	// digit must follow "/mnt/disk" so this does not also match "/mnt/disks/"
	// (the Unassigned Devices mount point), which holds legitimate appdata and
	// data that must be backed up.
	if rest := strings.TrimPrefix(norm, "/mnt/disk"); rest != norm && rest != "" && rest[0] >= '0' && rest[0] <= '9' {
		return true, "direct disk volume"
	}

	// Skip the Unraid root mount (/mnt) itself if mapped directly.
	if norm == "/mnt" {
		return true, "root /mnt mount"
	}

	// Everything else (e.g. /tmp or custom paths) — back up.
	return false, ""
}

// backupableMount reports whether a Docker mount TYPE holds real on-disk data
// worth archiving: bind mounts and volumes (a volume's Source is a real host
// path, /var/lib/docker/volumes/<name>/_data); tmpfs, npipe, cluster and image
// mounts are skipped. This is a type-only check — callers additionally apply
// restorableVolume to exclude anonymous volumes, which can't be reattached on
// restore. Named-volume support closes the appdata.backup plugin's single
// most-reported gap (compose stacks were previously backed up as empty).
func backupableMount(mountType string) bool {
	return mountType == "bind" || mountType == "volume"
}

// isHex64 reports whether s is a 64-character lowercase hex string — Docker's
// identifier format for an anonymous volume.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// restorableVolume reports whether a volume mount can be reliably restored.
// Only *named* volumes appear in the container's HostConfig.Binds (as
// "<name>:<dest>") and are reattached when the container is recreated;
// anonymous volumes (a 64-hex id, or no name) are not in Binds, so backing them
// up would orphan the data on restore. Such volumes are skipped to avoid a
// false sense of safety. Bind mounts are always restorable.
func restorableVolume(mountType, name string) bool {
	if mountType != "volume" {
		return true
	}
	return name != "" && !isHex64(name)
}

// sparseWarnMinBytes is the logical-size floor below which a sparse file isn't
// worth warning about — the read is fast regardless.
const sparseWarnMinBytes = 1 << 30 // 1 GiB

// sparseWarnRatio is the logical:physical ratio above which a large file is
// flagged as sparse.
const sparseWarnRatio = 10

// sparseInfo (defined per-platform in sparse_unix.go / sparse_other.go) reports
// whether a large regular file is sparse and returns its physical byte count.

// warnIfSparse logs a warning for a sparse file whose holes still force a full
// sequential read (the LMDB/Meilisearch multi-hour-stall class from the
// appdata.backup forum thread). It never errors; it only surfaces the risk so
// a slow backup isn't a silent mystery.
func warnIfSparse(path string, info os.FileInfo) {
	if sparse, physical := sparseInfo(info); sparse {
		log.Printf("engine: WARNING: %s is sparse (%s logical vs %s on disk); "+
			"the backup reads its full logical size and may be slow — consider excluding it",
			path, humanizeBytes(float64(info.Size())), humanizeBytes(float64(physical)))
	}
}

// networkDependency returns the container name/ID this container routes its
// network through (Docker's `network_mode: container:<x>`), or "" if none.
// Such a container (e.g. an app behind a Gluetun/VPN sidecar) must be started
// after its provider, so the runner warns when the job order would break that.
func networkDependency(inspect container.InspectResponse) string {
	if inspect.HostConfig == nil {
		return ""
	}
	mode := string(inspect.HostConfig.NetworkMode)
	if target, ok := strings.CutPrefix(mode, "container:"); ok {
		return target
	}
	return ""
}

// warnNetworkDependency logs a note when a container routes its network through
// another container (a Gluetun/VPN-sidecar setup). Vault backs up and restarts
// containers in the job's configured order, so the user must place the provider
// before its dependents or the dependent's network fails on restart.
func warnNetworkDependency(inspect container.InspectResponse, name string) {
	if dep := networkDependency(inspect); dep != "" {
		log.Printf("engine: NOTE: container %s routes its network through %q "+
			"(network_mode: container:%s) — make sure %q is in this job and ordered to "+
			"start first, or %s will lose its network when restarted after backup",
			name, dep, dep, dep, name)
	}
}

// isGlobPattern returns true if the pattern contains glob metacharacters.
func isGlobPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// shouldExcludePath returns true if relPath should be excluded based on the
// given exclusion patterns. Patterns without glob characters use prefix
// matching; patterns with glob characters use filepath.Match against both
// the full relative path and the base filename.
func shouldExcludePath(relPath string, exclusions []string) bool {
	if len(exclusions) == 0 || relPath == "" || relPath == "." {
		return false
	}

	for _, pattern := range exclusions {
		pattern = strings.TrimPrefix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			continue
		}

		if isGlobPattern(pattern) {
			// Match against full relative path.
			if matched, _ := filepath.Match(pattern, relPath); matched {
				return true
			}
			// Match against just the filename (so *.log works at any depth).
			if matched, _ := filepath.Match(pattern, filepath.Base(relPath)); matched {
				return true
			}
		} else {
			// Prefix match for directory exclusions.
			if relPath == pattern || strings.HasPrefix(relPath, pattern+"/") {
				return true
			}
		}
	}
	return false
}

// shouldExcludeMount reports whether a bind mount (directory or single file)
// should be excluded entirely based on user-provided exclusion patterns.
// Mirrors the matching logic of shouldExcludePath (used inside directory
// walks) so users get consistent semantics regardless of whether a bind
// mount is a directory or a single file. Supported pattern forms:
//
//   - Exact absolute path                  (e.g. /var/run/docker.sock, /rootfs)
//   - Parent directory (with or without /) (e.g. /var/run)
//   - Basename                             (e.g. docker.sock)
//   - Glob against full path or basename   (e.g. *docker.sock*, *.log)
//
// (issue #70)
func shouldExcludeMount(exclusions []string, mountDestination string) bool {
	if len(exclusions) == 0 || mountDestination == "" {
		return false
	}
	cleanDest := filepath.Clean(mountDestination)
	base := filepath.Base(cleanDest)

	for _, pattern := range exclusions {
		if pattern == "" {
			continue
		}
		if isGlobPattern(pattern) {
			if m, _ := filepath.Match(pattern, cleanDest); m {
				return true
			}
			if m, _ := filepath.Match(pattern, base); m {
				return true
			}
			continue
		}
		cleanPattern := filepath.Clean(pattern)
		if cleanPattern == cleanDest {
			return true
		}
		if strings.HasPrefix(cleanDest, cleanPattern+"/") {
			return true
		}
		if !strings.Contains(pattern, "/") && cleanPattern == base {
			return true
		}
	}
	return false
}

// mapExclusionsToVolume converts container-side exclusion paths into paths
// relative to a specific volume's mount destination. Glob patterns pass
// through unchanged since they apply to all volumes.
func mapExclusionsToVolume(exclusions []string, mountDestination string) []string {
	if len(exclusions) == 0 {
		return nil
	}

	mountDest := filepath.Clean(mountDestination)
	prefix := mountDest + "/"
	if mountDest == "/" {
		prefix = "/"
	}
	var mapped []string

	for _, excl := range exclusions {
		if isGlobPattern(excl) {
			mapped = append(mapped, excl)
			continue
		}

		cleanExcl := filepath.Clean(excl)
		if cleanExcl == mountDest {
			mapped = append(mapped, ".")
			continue
		}

		if strings.HasPrefix(cleanExcl, prefix) {
			rel := strings.TrimPrefix(cleanExcl, prefix)
			if rel != "" {
				mapped = append(mapped, rel)
			}
		}
	}

	return mapped
}

// ContainerHandler implements Handler for Docker containers.
//
// The cli field is held as the narrow dockerClient interface (not the
// concrete *client.Client) so unit tests can supply a mock without
// constructing the full moby client. The real moby *client.Client
// satisfies dockerClient via a compile-time assertion in
// docker_client.go.
type ContainerHandler struct {
	cli dockerClient
}

// NewContainerHandler creates a new ContainerHandler with a Docker client
// configured from environment variables.
func NewContainerHandler() (*ContainerHandler, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &ContainerHandler{cli: cli}, nil
}

// ListItems enumerates all Docker containers as BackupItems.
func (h *ContainerHandler) ListItems() ([]BackupItem, error) {
	ctx := context.Background()
	listResult, err := h.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	items := make([]BackupItem, 0, len(listResult.Items))
	for _, c := range listResult.Items {
		name := ShortID(c.ID)
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		settings := map[string]any{
			"id":    c.ID,
			"image": c.Image,
			"state": string(c.State),
		}
		// Advisory hint so the job wizard can offer a logical dump only where
		// one is plausible. Image-only: the container list carries no
		// environment, and inspecting every container to get it would put N API
		// calls behind this endpoint. The backup re-detects authoritatively.
		if kind := DetectDatabaseByImage(c.Image); kind != DatabaseNone {
			settings["database_kind"] = string(kind)
		}
		items = append(items, BackupItem{
			Name:     name,
			Type:     "container",
			Settings: settings,
		})
	}
	return items, nil
}

// DetectSocketMounts inspects the named container and returns a sorted list of
// in-container destination paths for any bind mount whose host source ends in
// ".sock" (e.g. /var/run/docker.sock, /var/run/docker-shim.sock). These paths
// are recommended exclusions because Go's archive/tar cannot serialize Unix
// socket inodes, and runtime sockets are never useful to back up.
//
// Returns an empty slice when the container has no socket mounts. Returns an
// error only when the inspect call itself fails (e.g. container not found).
func (h *ContainerHandler) DetectSocketMounts(ctx context.Context, name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	inspectResult, err := h.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspecting container %q: %w", name, err)
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, m := range inspectResult.Container.Mounts {
		if !strings.HasSuffix(m.Source, ".sock") {
			continue
		}
		dest := m.Destination
		if dest == "" {
			dest = m.Source
		}
		if _, ok := seen[dest]; ok {
			continue
		}
		seen[dest] = struct{}{}
		paths = append(paths, dest)
	}
	sort.Strings(paths)
	return paths, nil
}

// MountInfo describes a single container bind mount for the mounts API. It
// carries the auto-skip verdict from shouldSkipVolume so the UI can render
// heuristically-skipped mounts as disabled with an explanation.
type MountInfo struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Type        string `json:"type"`
	AutoSkip    bool   `json:"auto_skip"`
	SkipReason  string `json:"skip_reason,omitempty"`
}

// ListMounts inspects the named container and returns its backup-eligible
// mounts (bind mounts and named volumes), each annotated with the auto-skip
// verdict from shouldSkipVolume. Matches the engine's backup behaviour: tmpfs,
// device nodes, and anonymous volumes (which can't be reattached on restore)
// are excluded. Results are sorted by destination for stable UI ordering.
// labelExclusions controls whether the container's own vault.exclude label is
// evaluated. Passed in rather than read here because the engine holds no
// database handle — and discovery must agree with what the backup will
// actually do, or the wizard shows mounts as skipped that will in fact be
// backed up.
func (h *ContainerHandler) ListMounts(ctx context.Context, name string, labelExclusions bool) ([]MountInfo, error) {
	inspectResult, err := h.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspecting container %q: %w", name, err)
	}
	// Surface label-declared exclusions the same way as automatic skips, so the
	// wizard shows why a mount will not be backed up without the user having to
	// go and read the container's labels.
	var labelPatterns []string
	if labelExclusions {
		labelPatterns = parseLabelExclusions(containerLabels(inspectResult.Container))
	}

	mounts := make([]MountInfo, 0, len(inspectResult.Container.Mounts))
	for _, m := range inspectResult.Container.Mounts {
		if !backupableMount(string(m.Type)) || !restorableVolume(string(m.Type), m.Name) {
			continue
		}
		skip, reason := shouldSkipVolume(m.Source)
		if !skip && len(labelPatterns) > 0 && shouldExcludeMount(labelPatterns, filepath.Clean(m.Destination)) {
			skip = true
			reason = "excluded by the container's " + VaultExcludeLabel + " label"
		}
		mounts = append(mounts, MountInfo{
			Source:      m.Source,
			Destination: filepath.Clean(m.Destination),
			Type:        string(m.Type),
			AutoSkip:    skip,
			SkipReason:  reason,
		})
	}
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Destination < mounts[j].Destination
	})
	return mounts, nil
}

// extractExcludedMounts parses the excluded_mounts setting — the list of
// container-side mount destinations the user unchecked in the job wizard.
// Stored separately from exclude_paths so the UI can round-trip toggle state.
func extractExcludedMounts(settings map[string]any) []string {
	raw, ok := settings["excluded_mounts"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// containerLabels returns a container's labels, tolerating a nil Config —
// Docker omits it for some container states, and the rest of this file guards
// it the same way.
func containerLabels(inspect container.InspectResponse) map[string]string {
	if inspect.Config == nil {
		return nil
	}
	return inspect.Config.Labels
}

// VaultExcludeLabel is the Docker label a container uses to declare its own
// backup exclusions, e.g.
//
//	vault.exclude=/config/cache,/tmp
//
// Comma-separated so it reads the same as the free-text field in the job
// wizard, and the patterns are handed to the identical matcher — there is no
// second matching implementation to keep in sync.
//
// Declaring exclusions on the container means they travel with it: a compose
// file or template carries its own answer, and adding the container to a job
// needs no further configuration.
const VaultExcludeLabel = "vault.exclude"

// countExcludedMounts reports how many backup-eligible mounts exist and how
// many of them the exclusion list removes. Used to catch the case where a
// backup would succeed while containing no volume data at all.
func countExcludedMounts(mounts []container.MountPoint, exclusions []string) (eligible, excluded int) {
	for _, m := range mounts {
		if !backupableMount(string(m.Type)) || !restorableVolume(string(m.Type), m.Name) {
			continue
		}
		eligible++
		if shouldExcludeMount(exclusions, filepath.Clean(m.Destination)) {
			excluded++
		}
	}
	return eligible, excluded
}

// checkVolumeTargetCollisions rejects a custom-destination restore whose mounts
// would collide on a single directory.
//
// volumeRestoreTarget keys a custom target on the volume name or the BASE of the
// bind source, so two mounts with distinct sources but the same basename — say
// /mnt/user/app/config and /mnt/cache/other/config — both resolve to
// <dest>/config. Their trees would be merged, matching paths silently
// overwriting one another, and recreateAndStartContainer would then point both
// binds at that one directory. Failing before anything is written is the only
// safe answer: the alternative is a restore that reports success and hands back
// interleaved data.
//
// Only custom destinations are affected — with restoreDest empty every volume
// goes back to its own original source and collision is impossible.
func checkVolumeTargetCollisions(restoreDest string, mounts []mountInfo) error {
	if restoreDest == "" {
		return nil
	}
	seen := make(map[string]string, len(mounts))
	for _, m := range mounts {
		if !backupableMount(m.Type) || !restorableVolume(m.Type, m.Name) {
			continue
		}
		if m.Source == "" || m.Destination == "" {
			continue
		}
		target, err := volumeRestoreTarget(restoreDest, m.Type, m.Name, m.Source)
		if err != nil {
			return err
		}
		if prev, dup := seen[target]; dup {
			return fmt.Errorf("cannot restore to %s: mounts %q and %q both resolve to %q; "+
				"restore to their original locations instead, or restore one at a time",
				restoreDest, prev, m.Source, target)
		}
		seen[target] = m.Source
	}
	return nil
}

// catchAllExclusions are patterns that would exclude every mount. Rejected
// from labels — see parseLabelExclusions.
var catchAllExclusions = map[string]struct{}{
	"*": {}, "**": {}, "/": {}, "/*": {}, "/**": {}, ".": {},
}

// parseLabelExclusions extracts the exclusion patterns from a container's
// labels. Entries are trimmed and blanks dropped, so a trailing comma or
// padded list is tolerated rather than producing an empty pattern that would
// match everything.
//
// Catch-all patterns are refused. Unlike the exclusions an operator types into
// the job wizard, a label is authored by whoever wrote the image or template —
// not by the person who owns the backup. A single `vault.exclude=*` would
// otherwise exclude every mount (shouldExcludeMount globs against the mount's
// base name), and the backup would still report success while containing
// nothing. Narrow the blast radius: an image can opt paths out, it cannot opt
// the whole container out.
func parseLabelExclusions(labels map[string]string) []string {
	raw, ok := labels[VaultExcludeLabel]
	if !ok {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, catchAll := catchAllExclusions[p]; catchAll {
			log.Printf("engine: ignoring catch-all %s pattern %q — a label may exclude paths, not the whole container",
				VaultExcludeLabel, p)
			continue
		}
		out = append(out, p)
	}
	return out
}

// labelExclusionsEnabled reports whether the vault.exclude label should be
// honoured. Absent means enabled, matching the catalog default, so a caller
// that does not thread the setting still gets the documented behaviour.
func labelExclusionsEnabled(settings map[string]any) bool {
	v, ok := settings["label_exclusions_enabled"]
	if !ok {
		return true
	}
	b, _ := v.(bool)
	return b
}

// containerExclusionsWithLabels is containerExclusions plus any patterns the
// container declares via its vault.exclude label.
//
// Merged into the same slice on purpose: everything downstream — whole-mount
// skipping, volume-relative rewriting, the chunked walk — then treats a
// label-declared path exactly like a typed one.
func containerExclusionsWithLabels(settings map[string]any, labels map[string]string) []string {
	exclusions := containerExclusions(settings)
	if !labelExclusionsEnabled(settings) {
		return exclusions
	}
	return append(exclusions, parseLabelExclusions(labels)...)
}

// containerExclusions merges free-text exclude_paths with the checkbox-driven
// excluded_mounts list. Both feed the same shouldExcludeMount matching, so an
// unchecked mount is excluded exactly as a typed path would be.
func containerExclusions(settings map[string]any) []string {
	paths := extractExcludePaths(settings)
	mounts := extractExcludedMounts(settings)
	if len(mounts) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths)+len(mounts))
	out = append(out, paths...)
	out = append(out, mounts...)
	return out
}

// Backup performs a full backup of a Docker container:
//  1. Inspects the container and saves its config as JSON.
//  2. Stops the container if running (unless no_stop is set).
//  3. Saves the container image as image.tar.
//  4. Tars each bind mount volume to volume_N.tar.gz.
//  5. Restarts the container if it was stopped.
func (h *ContainerHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	// Tracks whether this run captured anything that could actually differ
	// from the parent restore point. config.json (the inspect dump) is written
	// on every run and so is deliberately not counted — it is what made an
	// incremental run of an idle container look like a real backup in the
	// history (issue #326).
	var wroteChangedContent bool

	containerID, _ := item.Settings["id"].(string)
	if containerID == "" {
		return nil, fmt.Errorf("container id not found in settings")
	}

	if err := os.MkdirAll(destDir, 0750); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	// Step 1: Inspect and save config.
	// Resolve the container by name first — container IDs change when
	// containers are recreated (updates, reboots, compose recreate).
	progress(item.Name, 10, "inspecting container")
	inspectResult, err := h.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		// Stored ID is stale — try resolving by container name instead.
		inspectResult, err = h.cli.ContainerInspect(ctx, item.Name, client.ContainerInspectOptions{})
		if err != nil {
			return nil, fmt.Errorf("inspecting container: %w", err)
		}
		containerID = inspectResult.Container.ID
		log.Printf("[backup] container %q: resolved by name (ID changed from stored value to %s)", item.Name, ShortID(containerID))
	}
	inspect := inspectResult.Container
	warnNetworkDependency(inspect, item.Name)

	configPath := filepath.Join(destDir, "config.json")
	configData, err := json.MarshalIndent(inspect, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling config: %w", err)
	}
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		return nil, fmt.Errorf("writing config: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(configPath))

	// Step 2: Stop container if running (unless no_stop setting).
	wasRunning := inspect.State.Running
	noStop, _ := item.Settings["no_stop"].(bool)
	changedSince, hasChangedSince := parseChangedSince(item.Settings)

	// Previous backup's per-volume effective listings (mount source host path ->
	// volume-relative path set), used to detect NEW files with stale mtimes in
	// differential/incremental runs (issue #320).
	prevBySource := prevVolumeListingSet(item.Settings)

	// Extract path exclusions from item settings: free-text exclude_paths plus
	// the checkbox-driven excluded_mounts from the job wizard, plus anything
	// the container declares for itself via the vault.exclude label.
	exclusions := containerExclusionsWithLabels(item.Settings, containerLabels(inspect))

	// Step 2a: logical database dump, BEFORE the container is stopped.
	//
	// A dump talks to the live server, so it has to happen while the container
	// is still up. It is written in addition to the volume archives — a dump
	// that turns out to be misconfigured must not cost the file-level backup
	// that would otherwise have been taken, which is also why a running server
	// is required rather than started for the purpose.
	if databaseDumpEnabled(item.Settings) {
		// State.Running is true for a container caught mid-restart, and exec
		// against one fails with "is restarting, wait until the container is
		// running" — seen on a live PostgreSQL container in a crash loop. Check
		// the status string so a flapping container is skipped with a clear
		// reason instead of failing the whole backup.
		if !wasRunning || inspect.State.Restarting {
			// The user explicitly asked for a dump, so omitting it silently
			// would hand back a backup missing what they asked for. Note that
			// the runner's stop_all mode stops every container before the
			// handler runs, which lands here — hence naming it.
			return nil, fmt.Errorf("database dump requested for %s but the container is %s; "+
				"a dump needs the server running — use the one-by-one container mode, or start the container",
				item.Name, containerStateDescription(string(inspect.State.Status), inspect.State.Restarting))
		} else if dumpFile, err := h.dumpDatabase(ctx, containerID, item.Name,
			inspect.Config.Image, inspect.Config.Env, destDir, item.Compression, progress); err != nil {
			return nil, err
		} else if dumpFile != nil {
			result.Files = append(result.Files, *dumpFile)
			// A dump is taken fresh on every run, so an item that dumps a
			// database always captured new content.
			wroteChangedContent = true
		}
	}

	// Differential runs: when no backupable volume changed since the parent
	// restore point, skip the stop/restart cycle entirely. The in-loop
	// per-volume pathChangedSince checks skip every volume anyway, so
	// stopping would be pure downtime with no consistency benefit. The image
	// save and template copy inside runWithRestart do NOT gate this decision
	// — docker save is daemon-side and the template is a file copy; both are
	// safe on a running container.
	needsStop := wasRunning && !noStop
	var volChanges map[string]bool
	if needsStop && hasChangedSince {
		// Accepted race: if a file changes between this pre-check and the
		// archiving loop, that volume is backed up live instead of
		// stopped. This matches pre-#253 behaviour but it's ultimately
		// an accepted risk due to how negligible it is.
		//
		// When the pre-check runs, per-volume results are cached so the
		// archiving loop reuses them instead of re-walking the same trees
		// via pathChangedSince.
		var anyChanged bool
		var err error
		volChanges, anyChanged, err = anyVolumeChangedSince(ctx, inspect.Mounts, exclusions, changedSince, prevBySource)
		if err != nil {
			return nil, err
		}
		if !anyChanged {
			log.Printf("engine: container %s volumes unchanged since %s — skipping stop/restart",
				item.Name, changedSince.Format(time.RFC3339))
			needsStop = false
		}
	}

	// needsStop is final here. A dump is only worth reloading over the restored
	// volume when that volume was read from a still-running server and so may be
	// torn — see DatabaseReplayMarker.
	if databaseDumpEnabled(item.Settings) && wasRunning && !needsStop {
		if err := writeDatabaseReplayMarker(destDir); err != nil {
			return nil, fmt.Errorf("recording database replay marker for %s: %w", item.Name, err)
		}
		// Everything that reaches the destination is registered here — a file
		// merely written into destDir is never uploaded.
		result.Files = append(result.Files, backupFileInfo(filepath.Join(destDir, DatabaseReplayMarker)))
	}

	if needsStop {
		progress(item.Name, 20, "stopping container")
		if _, err := h.cli.ContainerStop(ctx, containerID, client.ContainerStopOptions{}); err != nil {
			return nil, fmt.Errorf("stopping container %s: %w", item.Name, err)
		}
	}

	if err := runWithRestart(needsStop, item.Name, progress, func() error {
		// Step 3: Save image.
		includeImage := true
		if hasChangedSince {
			if createdAt, err := time.Parse(time.RFC3339Nano, inspect.Created); err == nil && !createdAt.After(changedSince) {
				includeImage = false
			}
		}
		if includeImage {
			progress(item.Name, 40, "saving image")
			imageName := inspect.Config.Image
			imgReader, err := h.cli.ImageSave(ctx, []string{imageName})
			if err != nil {
				return fmt.Errorf("saving image: %w", err)
			}
			imagePath := filepath.Join(destDir, "image.tar"+archiveExt(item.Compression))
			imgFile, err := os.Create(imagePath) // #nosec G304 — destDir is vault-controlled temp directory
			if err != nil {
				_ = imgReader.Close()
				return fmt.Errorf("creating image file: %w", err)
			}
			cw, closeCompress, cwErr := compressedWriter(imgFile, item.Compression)
			if cwErr != nil {
				_ = imgFile.Close()
				_ = imgReader.Close()
				return cwErr
			}
			if _, err := io.Copy(cw, imgReader); err != nil {
				_ = closeCompress()
				_ = imgFile.Close()
				_ = imgReader.Close()
				return fmt.Errorf("writing image: %w", err)
			}
			if err := closeCompress(); err != nil {
				_ = imgFile.Close()
				_ = imgReader.Close()
				return fmt.Errorf("finalising image compression: %w", err)
			}
			_ = imgFile.Close()
			_ = imgReader.Close()
			result.Files = append(result.Files, backupFileInfo(imagePath))
			wroteChangedContent = true

			// Capture image RepoDigests so restore can repopulate Unraid's
			// docker update-status.json. `docker load` (used on restore) does
			// not preserve RepoDigests, so without this the Unraid Docker
			// Manager UI shows "not available" for the restored container and
			// "Check for Updates" cannot resolve a local digest.
			if metaPath := filepath.Join(destDir, "image_meta.json"); metaPath != "" {
				meta := buildImageMeta(ctx, h.cli, imageName)
				if metaBytes, mErr := json.MarshalIndent(meta, "", "  "); mErr == nil {
					_ = os.WriteFile(metaPath, metaBytes, 0600) // #nosec G306 — metadata file, ok world-readable
					result.Files = append(result.Files, backupFileInfo(metaPath))
				}
			}
		}

		// Step 4: Tar bind mounts and named volumes, skipping large shared data paths.
		progress(item.Name, 60, "backing up volumes")
		// A container whose every eligible mount is excluded would otherwise
		// produce a "successful" backup holding only config and image — the
		// same silent data-loss trap the VM handler guards against when a
		// domain has no file-backed disks. Reachable through a broad global
		// rule or a container's own label, neither of which the person running
		// the job necessarily wrote, so fail loudly instead.
		if eligible, excluded := countExcludedMounts(inspect.Mounts, exclusions); eligible > 0 && excluded == eligible {
			return fmt.Errorf("every backup-eligible mount of container %s is excluded (%d of %d); "+
				"check the job's exclusions, the global exclusion list, and any %s label on the container",
				item.Name, excluded, eligible, VaultExcludeLabel)
		}
		var manifest []volumeManifestEntry
		for i, mount := range inspect.Mounts {
			if !backupableMount(string(mount.Type)) {
				continue
			}
			if mount.Source == "" || mount.Destination == "" {
				continue
			}

			entry := volumeManifestEntry{
				Index:       i,
				Source:      mount.Source,
				Destination: mount.Destination,
			}

			if !restorableVolume(string(mount.Type), mount.Name) {
				log.Printf("engine: skipping anonymous volume %q for %s (not restorable — no stable name)", mount.Destination, item.Name)
				entry.BackedUp = false
				entry.SkipReason = "anonymous volume (not restorable)"
				manifest = append(manifest, entry)
				continue
			}

			if skip, reason := shouldSkipVolume(mount.Source); skip {
				log.Printf("engine: skipping volume %s for %s: %s", mount.Source, item.Name, reason)
				entry.BackedUp = false
				entry.SkipReason = reason
				manifest = append(manifest, entry)
				continue
			}

			// Honour exclusion patterns at the volume level for BOTH directory
			// and file mounts. Historically this check only applied to
			// file-based mounts, which meant exclusions like `/rootfs` for
			// containers that bind-mount the host root (Telegraf, Netdata,
			// cAdvisor, Glances, node-exporter) were ignored — the engine
			// recursed into the entire host filesystem and hung. Applying the
			// check up-front matches the semantics users expect from the UI
			// hint ("absolute container paths"). (issue #70)
			//
			// CRITICAL: this check MUST run before any work that touches
			// mount.Source on disk. Previously MaybeDowngradeCompression was
			// called first; for a `/` → `/rootfs` mount that meant a
			// filepath.Walk of the entire host filesystem before the volume
			// was skipped, regressing the original #70 fix (a 37 s Test
			// Containers job degraded to 14 minutes). The differential
			// pathChangedSince pre-scan below is another such walk, so it
			// likewise runs only after the exclusion check.
			if shouldExcludeMount(exclusions, mount.Destination) {
				log.Printf("engine: skipping volume %s for %s: matches exclusion pattern", mount.Source, item.Name)
				entry.BackedUp = false
				entry.SkipReason = "matches exclusion pattern"
				manifest = append(manifest, entry)
				continue
			}

			if hasChangedSince {
				changed, cached := volChanges[mount.Source]
				if !cached {
					var err error
					changed, err = pathChangedSinceWithPrev(ctx, mount.Source, changedSince, prevBySource[mount.Source])
					if err != nil {
						return fmt.Errorf("checking volume %s changes: %w", mount.Source, err)
					}
				}
				if !changed {
					entry.BackedUp = false
					entry.SkipReason = "unchanged since reference"
					manifest = append(manifest, entry)
					continue
				}
			}

			// Detect file-based bind mounts (e.g. Tailscale hook files).
			srcInfo, err := os.Lstat(mount.Source)
			if err != nil {
				return fmt.Errorf("stat volume %s: %w", mount.Source, err)
			}

			// Auto-downgrade compression for media-heavy volumes (single-file
			// bind mounts are auto-handled too — the helper inspects whatever
			// path it's given). Saves CPU on Immich/Jellyfin/etc. appdata.
			effectiveCompression := MaybeDowngradeCompression(mount.Source, item.Compression)
			archiveName := fmt.Sprintf("volume_%d.tar%s", i, archiveExt(effectiveCompression))
			volDest := filepath.Join(destDir, archiveName)

			if srcInfo.IsDir() {
				volExclusions := mapExclusionsToVolume(exclusions, mount.Destination)

				if hasChangedSince {
					if err := tarDirectoryFilteredWithPrev(ctx, mount.Source, volDest, changedSince, volExclusions, effectiveCompression, prevBySource[mount.Source]); err != nil {
						return fmt.Errorf("archiving volume %s: %w", mount.Source, err)
					}
				} else {
					if err := tarDirectory(ctx, mount.Source, volDest, volExclusions, effectiveCompression); err != nil {
						return fmt.Errorf("archiving volume %s: %w", mount.Source, err)
					}
				}

				if len(volExclusions) > 0 {
					entry.ExcludedPaths = volExclusions
				}

				// Authoritative point-in-time per-volume listing (full effective
				// file set after exclusions) — lets the NEXT differential detect
				// NEW files with stale mtimes (issue #320). Best-effort, mirroring
				// folder.go's WriteEffectiveListing usage.
				if err := WriteEffectiveListing(mount.Source, volDest, volExclusions); err != nil {
					log.Printf("engine: warning: failed to write volume listing for %s: %v", mount.Source, err)
				} else {
					result.Files = append(result.Files, backupFileInfo(volDest+ListingSuffix))
				}
			} else {
				// Auto-skip non-regular inodes (sockets, named pipes, devices,
				// irregular). archive/tar refuses to write headers for these
				// types and would fail the entire backup with errors like
				// "archive/tar: sockets not supported". Container bind mounts
				// to runtime sockets such as /var/run/docker.sock are never
				// useful to back up — skip them with a clear reason rather
				// than aborting the whole job (issue #70).
				if srcInfo.Mode()&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice|os.ModeCharDevice|os.ModeIrregular) != 0 {
					reason := fmt.Sprintf("unsupported inode type (%s)", srcInfo.Mode().Type().String())
					log.Printf("engine: skipping volume %s for %s: %s", mount.Source, item.Name, reason)
					entry.BackedUp = false
					entry.SkipReason = reason
					manifest = append(manifest, entry)
					continue
				}

				// Honour exclusion patterns for file-based bind mounts is
				// already handled at the volume level above via
				// shouldExcludeMount (issue #70).

				if err := tarFile(ctx, mount.Source, volDest, effectiveCompression); err != nil {
					return fmt.Errorf("archiving volume file %s: %w", mount.Source, err)
				}
				entry.IsFile = true
			}

			result.Files = append(result.Files, backupFileInfo(volDest))
			entry.BackedUp = true
			wroteChangedContent = true
			entry.Archive = archiveName
			manifest = append(manifest, entry)

			// Best-effort tar index sidecar for partial restore. Only
			// useful for directory tars (volume containing multiple
			// files); single-file bind-mount archives are skipped via
			// the entry.IsFile flag because there is nothing to index.
			if !entry.IsFile {
				if err := WriteTarIndex(volDest); err == nil {
					result.Files = append(result.Files, backupFileInfo(volDest+IndexSuffix))
				}
			}
		}

		// Save volumes manifest so restore knows which mounts were backed up.
		if len(manifest) > 0 {
			manifestData, _ := json.MarshalIndent(manifest, "", "  ")
			manifestPath := filepath.Join(destDir, "volumes.json")
			if err := os.WriteFile(manifestPath, manifestData, 0600); err != nil {
				log.Printf("engine: warning: failed to write volumes manifest: %v", err)
			}
		}

		// Step 5: Save Unraid template XML if it exists.
		// The template is used by the Unraid Docker Manager (Community Apps) to
		// recognize and manage the container. Path pattern:
		//   /boot/config/plugins/dockerMan/templates-user/my-<name>.xml
		templatePath := filepath.Join("/boot/config/plugins/dockerMan/templates-user", "my-"+item.Name+".xml")
		if data, err := os.ReadFile(templatePath); err == nil { // #nosec G304 G703 — templatePath is fixed base dir + item.Name from Docker inspect
			includeTemplate := true
			if hasChangedSince {
				changed, changeErr := pathChangedSince(ctx, templatePath, changedSince)
				if changeErr != nil {
					return fmt.Errorf("checking template changes: %w", changeErr)
				}
				includeTemplate = changed
			}
			if includeTemplate {
				progress(item.Name, 85, "saving template")
				destTemplate := filepath.Join(destDir, "template.xml")
				if writeErr := os.WriteFile(destTemplate, data, 0600); writeErr != nil { // #nosec G703 — data from Docker template, destTemplate is destDir + fixed filename
					return fmt.Errorf("writing template xml: %w", writeErr)
				}
				result.Files = append(result.Files, backupFileInfo(destTemplate))
				wroteChangedContent = true
			}
		}

		return nil
	}, func() error {
		restartCtx, cancel := restartCleanupCtx(ctx)
		defer cancel()
		_, err := h.cli.ContainerStart(restartCtx, containerID, client.ContainerStartOptions{})
		return err
	}); err != nil {
		return nil, err
	}

	progress(item.Name, 100, "backup complete")
	// A differential/incremental run that captured no changed content is a
	// successful backup of nothing: the container stays restorable through the
	// chain, but the history must not present it as freshly backed up
	// (issue #326).
	if hasChangedSince && !wroteChangedContent {
		if result.Meta == nil {
			result.Meta = map[string]any{}
		}
		result.Meta[MetaUnchanged] = true
	}
	result.Success = true
	return result, nil
}

// restartCleanupCtx returns a bounded context for the post-backup container
// restart that is detached from cancellation of the backup context. Without
// this, a cancelled backup (operator Cancel or the stall watchdog) would also
// fail the restart — the Moby client ties ContainerStart to its request
// context — leaving a previously-running container stopped. The timeout keeps
// cleanup from hanging on an unresponsive daemon. See issue #251.
func restartCleanupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
}

// runWithRestart ensures a previously running container is started again even
// if later backup steps fail after the stop operation has already succeeded.
func runWithRestart(shouldRestart bool, itemName string, progress ProgressFunc, run func() error, restart func() error) error {
	runErr := run()
	if !shouldRestart {
		return runErr
	}

	if progress != nil {
		progress(itemName, 92, "restarting container")
	}
	restartErr := restart()
	if restartErr == nil {
		return runErr
	}

	wrappedRestartErr := fmt.Errorf("restarting container: %w", restartErr)
	if runErr != nil {
		return errors.Join(runErr, wrappedRestartErr)
	}

	return wrappedRestartErr
}

// anyVolumeChangedSince reports whether at least one backupable, restorable,
// non-skipped, non-excluded volume in mounts has been modified after
// changedSince. It applies the same skip filters — in the same order — as
// the Backup and BackupChunked volume loops: exclusions are checked BEFORE
// pathChangedSince so an excluded host-root bind mount (/ → /rootfs —
// Telegraf, Netdata, Glances) is never walked (issues #70, #251).
//
// prevBySource maps a mount source host path to the paths recorded in the
// parent restore point's per-volume effective listing; a file absent from that
// listing is treated as NEW (and therefore changed) even when its mtime
// predates changedSince (issue #320). A nil map degrades to mtime-only.
//
// Callers use this to decide whether a differential run needs to stop the
// container at all: when no volume changed, the in-loop per-volume
// pathChangedSince checks skip every volume anyway, so the stop/restart
// cycle would be pure downtime with no consistency benefit.
func anyVolumeChangedSince(ctx context.Context, mounts []container.MountPoint, exclusions []string, changedSince time.Time, prevBySource map[string]map[string]struct{}) (map[string]bool, bool, error) {
	changes := make(map[string]bool)
	anyChanged := false
	for _, mnt := range mounts {
		if !backupableMount(string(mnt.Type)) {
			continue
		}
		if mnt.Source == "" || mnt.Destination == "" {
			continue
		}
		if !restorableVolume(string(mnt.Type), mnt.Name) {
			continue
		}
		if skip, _ := shouldSkipVolume(mnt.Source); skip {
			continue
		}
		if shouldExcludeMount(exclusions, mnt.Destination) {
			continue
		}
		changed, err := pathChangedSinceWithPrev(ctx, mnt.Source, changedSince, prevBySource[mnt.Source])
		if err != nil {
			return nil, false, fmt.Errorf("checking volume %s changes: %w", mnt.Source, err)
		}
		changes[mnt.Source] = changed
		if changed {
			anyChanged = true
		}
	}
	return changes, anyChanged, nil
}

// chunkedPrevBySource derives the per-volume previous listing (mount source
// host path -> volume-relative path set) from the parent manifest's volume
// sub-manifests, mirroring the classic path's prev_volume_listing_paths. Used
// by the chunked (dedup) stop decision so a NEW stale-mtime file flips an
// otherwise-unchanged volume to changed (issue #320) — the same semantics the
// classic Backup path's prev-aware stop check already has. A nil parent or a
// nil repo omits every volume. A volume whose parent entry is missing, holds
// the skip sentinel, or whose sub-manifest is unreadable gets an EMPTY set:
// every pre-existing file then reports as absent-from-parent (changed), which
// forces the stop decision to keep needsStop set. Omitting the volume instead
// would let the pre-check report the volume unchanged (mtime-only) and skip
// the stop, while the archiving loop below later takes the full-chunk
// fallback on a still-running container — an inconsistent live backup.
func chunkedPrevBySource(parent *dedup.Manifest, mounts []container.MountPoint, repo *dedup.Repo) map[string]map[string]struct{} {
	if parent == nil || repo == nil {
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, mnt := range mounts {
		if !backupableMount(string(mnt.Type)) || mnt.Source == "" || mnt.Destination == "" {
			continue
		}
		pe, ok := parent.Files[containerVolPrefix+mnt.Destination]
		if !ok || len(pe.Chunks) == 0 {
			out[mnt.Source] = map[string]struct{}{}
			continue
		}
		sub, err := repo.GetManifest(pe.Chunks[0])
		if err != nil {
			out[mnt.Source] = map[string]struct{}{}
			continue
		}
		set := make(map[string]struct{}, len(sub.Files))
		for p := range sub.Files {
			set[p] = struct{}{}
		}
		out[mnt.Source] = set
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Restore restores a Docker container from a backup directory:
//  1. Loads the image from image.tar.
//  2. Reads the saved config (full docker inspect JSON).
//  3. Restores volume data from tar.gz archives.
//  4. Recreates the container from the saved configuration.
//  5. Restores the Unraid template XML (if present) so the Docker Manager
//     recognizes the container.
//  6. Starts the container if it was originally running.
//
// If item.Settings["restore_destination"] is set, volumes are extracted
// under that base directory instead of their original paths.
func (h *ContainerHandler) Restore(ctx context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {

	// Step 1: Load image.
	progress(item.Name, 5, "loading image")
	imagePath, err := findArchive(sourceDir, "image.tar")
	if err != nil {
		return fmt.Errorf("locating image archive: %w", err)
	}
	imgFile, err := os.Open(imagePath) // #nosec G304 — sourceDir is vault-controlled temp directory; findArchive only returns sourceDir-rooted paths
	if err != nil {
		return fmt.Errorf("opening image file: %w", err)
	}
	defer imgFile.Close()

	// Auto-detect compression so we can hand a plain-tar stream to docker.
	imgReader, closeImgDecompress, err := detectingReader(imgFile)
	if err != nil {
		return err
	}
	defer func() { _ = closeImgDecompress() }()

	resp, err := h.cli.ImageLoad(ctx, imgReader)
	if err != nil {
		return fmt.Errorf("loading image: %w", err)
	}
	// Drain the response to ensure the daemon completes the load.
	_, _ = io.Copy(io.Discard, resp)
	_ = resp.Close()

	// Step 2: Read full container config.
	progress(item.Name, 15, "reading config")
	configPath := filepath.Join(sourceDir, "config.json")
	configData, err := os.ReadFile(configPath) // #nosec G304 — sourceDir is vault-controlled temp directory
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	// Decode using the shared partial-struct shape so both the classic
	// (tar-on-disk) restore and the dedup-chunked restore unmarshal the
	// same fields. See restoreInspect for the full shape.
	var inspect restoreInspect
	if err := json.Unmarshal(configData, &inspect); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Check for alternate restore destination.
	restoreDest, _ := item.Settings["restore_destination"].(string)
	if restoreDest != "" {
		normalizedRestoreDest, err := normalizeRestorePath(restoreDest)
		if err != nil {
			return err
		}
		restoreDest = normalizedRestoreDest
	}
	if err := checkVolumeTargetCollisions(restoreDest, inspect.mountInfos()); err != nil {
		return err
	}

	// Step 3: Restore volumes.
	// Load the volumes manifest (if present) to know which were skipped.
	progress(item.Name, 30, "restoring volumes")
	var savedManifest []volumeManifestEntry
	if mData, err := os.ReadFile(filepath.Join(sourceDir, "volumes.json")); err == nil { // #nosec G304 — sourceDir is vault-controlled temp directory
		_ = json.Unmarshal(mData, &savedManifest)
	}

	for i, mount := range inspect.Mounts {
		if !backupableMount(mount.Type) {
			continue
		}
		volArchive, err := findArchive(sourceDir, fmt.Sprintf("volume_%d.tar", i))
		if err != nil {
			// Check manifest to explain why.
			for _, me := range savedManifest {
				if me.Index == i && !me.BackedUp {
					log.Printf("engine: restore: skipping volume %s (was excluded: %s)", mount.Source, me.SkipReason)
					break
				}
			}
			continue // skip if archive doesn't exist
		}

		// For named volumes the recreated container's bind is rewritten to
		// restoreDest/<volume-name> (see recreateAndStartContainer), so the
		// data must land there too — not restoreDest/_data.
		// Collisions are rejected up-front by checkVolumeTargetCollisions.
		targetPath, err := volumeRestoreTarget(restoreDest, mount.Type, mount.Name, mount.Source)
		if err != nil {
			return err
		}
		normalizedTargetPath, err := normalizeRestorePath(targetPath)
		if err != nil {
			return err
		}
		targetPath = normalizedTargetPath

		// Check manifest to determine if this was a file-based mount.
		isFileMount := false
		for _, me := range savedManifest {
			if me.Index == i && me.IsFile {
				isFileMount = true
				break
			}
		}

		if isFileMount {
			if err := os.MkdirAll(filepath.Dir(targetPath), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", targetPath, err)
			}
			if err := untarFile(ctx, volArchive, targetPath); err != nil {
				return fmt.Errorf("restoring volume file %s: %w", targetPath, err)
			}
		} else {
			if err := os.MkdirAll(targetPath, 0750); err != nil {
				return fmt.Errorf("creating volume dir %s: %w", targetPath, err)
			}
			if err := untarDirectory(ctx, volArchive, targetPath); err != nil {
				return fmt.Errorf("restoring volume %s: %w", targetPath, err)
			}
		}
	}

	// Step 4-6: Recreate the container, restore the Unraid template XML,
	// repopulate update-status, and start the container if it was running.
	// Hoisted into recreateAndStartContainer so the dedup-chunked restore
	// path can reuse the exact same sequence.
	if err := h.recreateAndStartContainer(ctx, item, inspect, restoreDest, sourceDir, progress); err != nil {
		return err
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

// restoreInspect is the partial-struct shape used to decode the saved
// container inspect JSON. It carries only the fields we actually need to
// recreate the container; the rest are intentionally dropped so we stay
// resilient against Docker API version churn. Used by both the classic
// (tar-on-disk) and dedup-chunked restore paths so the two share one
// container-recreation pipeline.
type restoreInspect struct {
	Name   string `json:"Name"`
	Config struct {
		Hostname     string            `json:"Hostname"`
		Domainname   string            `json:"Domainname"`
		User         string            `json:"User"`
		Env          []string          `json:"Env"`
		Cmd          []string          `json:"Cmd"`
		Entrypoint   []string          `json:"Entrypoint"`
		Image        string            `json:"Image"`
		Labels       map[string]string `json:"Labels"`
		WorkingDir   string            `json:"WorkingDir"`
		ExposedPorts map[string]any    `json:"ExposedPorts"`
	} `json:"Config"`
	HostConfig struct {
		Binds        []string `json:"Binds"`
		NetworkMode  string   `json:"NetworkMode"`
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Privileged bool     `json:"Privileged"`
		CapAdd     []string `json:"CapAdd"`
		CapDrop    []string `json:"CapDrop"`
		Dns        []string `json:"Dns"`
		DnsSearch  []string `json:"DnsSearch"`
		ExtraHosts []string `json:"ExtraHosts"`
		IpcMode    string   `json:"IpcMode"`
		PidMode    string   `json:"PidMode"`
		Devices    []struct {
			PathOnHost        string `json:"PathOnHost"`
			PathInContainer   string `json:"PathInContainer"`
			CgroupPermissions string `json:"CgroupPermissions"`
		} `json:"Devices"`
		Tmpfs      map[string]string `json:"Tmpfs"`
		ShmSize    int64             `json:"ShmSize"`
		CpusetCpus string            `json:"CpusetCpus"`
		Memory     int64             `json:"Memory"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAMConfig *struct {
				IPv4Address string `json:"IPv4Address"`
			} `json:"IPAMConfig"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// mountInfo carries the mount fields needed to route a chunked volume
// restore to the correct target directory. It mirrors the anonymous struct
// in restoreInspect.Mounts but as a named type so it can be passed in maps.
type mountInfo struct {
	Type        string
	Name        string
	Source      string
	Destination string
}

// mountInfos converts the anonymous mount structs into the named type, so both
// restore paths can share helpers that take a mount list.
func (ri restoreInspect) mountInfos() []mountInfo {
	out := make([]mountInfo, 0, len(ri.Mounts))
	for _, m := range ri.Mounts {
		out = append(out, mountInfo{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	return out
}

// recreateAndStartContainer is the shared post-volume-restore pipeline:
//
//  1. Remove any existing container with the same name.
//  2. Build typed Config / HostConfig / NetworkingConfig from the decoded
//     inspect struct (rewriting bind source paths when restoring to an
//     alternate destination).
//  3. Create the container.
//  4. Restore the Unraid template XML (if sourceDir is non-empty and a
//     template.xml sidecar exists). The dedup-chunked path passes
//     sourceDir="" because no template is captured in that scope.
//  5. Repopulate Unraid's docker update-status cache so the "Check for
//     Updates" UI badge works after restore.
//  6. Start the container if it was originally running.
//
// Hoisted from the classic Restore method so the dedup-chunked
// RestoreChunked path uses the same code, avoiding a 200-LOC copy.
func (h *ContainerHandler) recreateAndStartContainer(ctx context.Context, item BackupItem, inspect restoreInspect, restoreDest, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 55, "recreating container")
	containerName := strings.TrimPrefix(inspect.Name, "/")
	if containerName == "" {
		containerName = item.Name
	}
	safeContainerName, err := normalizeRestoreComponent(containerName)
	if err != nil {
		return err
	}
	containerName = safeContainerName

	// Remove existing container with the same name if present.
	if existResult, err := h.cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{}); err == nil {
		existing := existResult.Container
		if existing.State != nil && existing.State.Running {
			_, _ = h.cli.ContainerStop(ctx, existing.ID, client.ContainerStopOptions{})
		}
		_, _ = h.cli.ContainerRemove(ctx, existing.ID, client.ContainerRemoveOptions{Force: true})
	}

	// Build container config.
	// Convert the inspected ExposedPorts (raw `"<port>/<proto>": {}` JSON
	// from the original container) into the typed PortSet the Docker API
	// requires. Without this the restored container has no exposed ports,
	// which causes the Unraid Docker page to show blank "Container Port"
	// and "LAN IP:Port" columns until the user opens Edit → Done.
	exposedPorts := network.PortSet{}
	for portKey := range inspect.Config.ExposedPorts {
		p, err := network.ParsePort(portKey)
		if err != nil {
			log.Printf("engine: restore: skipping malformed exposed port %q for %s: %v", portKey, item.Name, err)
			continue
		}
		exposedPorts[p] = struct{}{}
	}

	containerConfig := &container.Config{
		Hostname:     inspect.Config.Hostname,
		Domainname:   inspect.Config.Domainname,
		User:         inspect.Config.User,
		Env:          inspect.Config.Env,
		Cmd:          inspect.Config.Cmd,
		Entrypoint:   inspect.Config.Entrypoint,
		Image:        inspect.Config.Image,
		Labels:       inspect.Config.Labels,
		WorkingDir:   inspect.Config.WorkingDir,
		ExposedPorts: exposedPorts,
	}

	// Build host config.
	binds := inspect.HostConfig.Binds
	// If restoring to an alternate destination, rewrite bind source paths.
	if restoreDest != "" {
		rewritten := make([]string, 0, len(binds))
		for _, bind := range binds {
			parts := strings.SplitN(bind, ":", 2)
			if len(parts) == 2 {
				sourceName, err := normalizeRestoreComponent(filepath.Base(parts[0]))
				if err != nil {
					return err
				}
				newSource := filepath.Join(restoreDest, sourceName)
				rewritten = append(rewritten, newSource+":"+parts[1])
			} else {
				rewritten = append(rewritten, bind)
			}
		}
		binds = rewritten
	}

	// Convert the inspected PortBindings (raw `"<port>/<proto>": [{HostIp, HostPort}]`
	// JSON from the original container) into the typed network.PortMap the
	// Docker API requires. This is what populates the "LAN IP:Port" column
	// on the Unraid Docker page — without it the restored container has no
	// host-port bindings and the column shows blank until the user opens
	// Edit → Done in the Unraid UI.
	portBindings := network.PortMap{}
	for portKey, bindings := range inspect.HostConfig.PortBindings {
		p, err := network.ParsePort(portKey)
		if err != nil {
			log.Printf("engine: restore: skipping malformed port binding %q for %s: %v", portKey, item.Name, err)
			continue
		}
		converted := make([]network.PortBinding, 0, len(bindings))
		for _, b := range bindings {
			pb := network.PortBinding{HostPort: b.HostPort}
			if b.HostIP != "" {
				if addr, parseErr := netip.ParseAddr(b.HostIP); parseErr == nil {
					pb.HostIP = addr
				}
				// Empty/invalid HostIP is left as the zero netip.Addr,
				// which Docker treats as "bind on all interfaces" —
				// matching docker's own default behaviour.
			}
			converted = append(converted, pb)
		}
		portBindings[p] = converted
	}

	hostConfig := &container.HostConfig{
		Binds:        binds,
		NetworkMode:  container.NetworkMode(inspect.HostConfig.NetworkMode),
		PortBindings: portBindings,
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyMode(inspect.HostConfig.RestartPolicy.Name),
			MaximumRetryCount: inspect.HostConfig.RestartPolicy.MaximumRetryCount,
		},
		Privileged: inspect.HostConfig.Privileged,
		CapAdd:     inspect.HostConfig.CapAdd,
		CapDrop:    inspect.HostConfig.CapDrop,
		DNS:        parseDNSAddrs(inspect.HostConfig.Dns),
		DNSSearch:  inspect.HostConfig.DnsSearch,
		ExtraHosts: inspect.HostConfig.ExtraHosts,
		IpcMode:    container.IpcMode(inspect.HostConfig.IpcMode),
		PidMode:    container.PidMode(inspect.HostConfig.PidMode),
		Tmpfs:      inspect.HostConfig.Tmpfs,
		ShmSize:    inspect.HostConfig.ShmSize,
		Resources: container.Resources{
			CpusetCpus: inspect.HostConfig.CpusetCpus,
			Memory:     inspect.HostConfig.Memory,
		},
	}

	// Convert devices.
	for _, d := range inspect.HostConfig.Devices {
		hostConfig.Devices = append(hostConfig.Devices, container.DeviceMapping{
			PathOnHost:        d.PathOnHost,
			PathInContainer:   d.PathInContainer,
			CgroupPermissions: d.CgroupPermissions,
		})
	}

	// Build networking config with endpoint settings.
	networkConfig := &network.NetworkingConfig{}
	if len(inspect.NetworkSettings.Networks) > 0 {
		networkConfig.EndpointsConfig = make(map[string]*network.EndpointSettings)
		for netName, netCfg := range inspect.NetworkSettings.Networks {
			es := &network.EndpointSettings{}
			if netCfg.IPAMConfig != nil && netCfg.IPAMConfig.IPv4Address != "" {
				if addr, err := netip.ParseAddr(netCfg.IPAMConfig.IPv4Address); err == nil {
					es.IPAMConfig = &network.EndpointIPAMConfig{
						IPv4Address: addr,
					}
				}
			}
			networkConfig.EndpointsConfig[netName] = es
		}
	}

	created, err := h.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: networkConfig,
		Name:             containerName,
	})
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}

	// Step 5: Restore Unraid template XML (classic path only; the
	// dedup-chunked path passes sourceDir="" because no template sidecar
	// is captured in volumes-only scope).
	if sourceDir != "" {
		progress(item.Name, 80, "restoring template")
		templateSrc := filepath.Join(sourceDir, "template.xml")
		if data, readErr := os.ReadFile(templateSrc); readErr == nil { // #nosec G304 — sourceDir is vault-controlled temp directory
			templateDest := filepath.Join("/boot/config/plugins/dockerMan/templates-user", "my-"+containerName+".xml") // #nosec G703 //nolint:gosec // path is constructed from trusted container name
			if mkErr := os.MkdirAll(filepath.Dir(templateDest), 0750); mkErr == nil {
				_ = os.WriteFile(templateDest, data, 0600) // #nosec G703 //nolint:gosec // best-effort restore of template
			}
		}
	}

	// Step 5b: Repopulate Unraid's docker update-status so the "Check for
	// Updates" feature works for the restored container. `docker load`
	// doesn't preserve RepoDigests, so the Unraid Docker Manager would
	// otherwise be unable to compute a local digest and show "not available".
	//
	// Strategy: first try to refresh RepoDigests via a `docker pull` —
	// non-destructive when the layers we just loaded are already up to date
	// with the registry, and it lets Docker populate RepoDigests natively
	// (which is the only thing Unraid actually reads). If the pull succeeds
	// the JSON-meta seeding is still safe to run as a confirmation, but
	// when it fails (offline / private registry / etc.) we fall back to
	// seeding from `image_meta.json` captured at backup time.
	_ = populateRepoDigestsViaPull(ctx, h.cli, inspect.Config.Image)
	if sourceDir != "" {
		restoreUnraidUpdateStatus(sourceDir, inspect.Config.Image)
	}

	// Step 6: Start container if it was originally running.
	// A database dump the backup carried only ever exists inside sourceDir,
	// the temporary restore staging directory — it is a sidecar of the
	// backup, not part of any restored volume. Track it here so both the
	// running (reload) and the leave-in-place paths can act on it, and so it
	// is preserved somewhere durable before the staging dir is cleaned up.
	var pendingDump string
	if sourceDir != "" {
		pendingDump = findDatabaseDump(sourceDir)
	}
	// Reason the dump ends up preserved rather than replayed, for the
	// operator-facing log in Step 8. Defaults to the running-but-volume-newer
	// case; overridden below for the not-running and not-ready cases.
	dumpReason := "the restored volume is the newer, consistent copy"
	if !inspect.State.Running {
		dumpReason = "the container was not running"
	}

	if inspect.State.Running {
		progress(item.Name, 90, "starting container")
		if _, err := h.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("starting restored container: %w", err)
		}

		// Step 7: reload a logical database dump, if the backup requested it.
		//
		// A failure here is NOT swallowed. The reload replays into the server
		// restored from its volume, and the dump's own DROP/replace statements
		// mutate that state as soon as it starts — so once it has begun, the
		// file-level result is no longer an intact fallback. Reporting success
		// over a half-applied dump would hand back a database that looks
		// restored and is not.
		//
		// Failing to become ready is different: nothing has been replayed, the
		// volume restore stands on its own, so that is a warning and the dump
		// is preserved below rather than lost.
		if pendingDump != "" && databaseReplayRequested(sourceDir) {
			progress(item.Name, 95, "reloading database dump")
			if err := h.waitForDatabaseReady(ctx, created.ID, inspect.Config.Image, inspect.Config.Env); err != nil {
				log.Printf("engine: %s: WARNING database did not become ready, dump not reloaded (the file-level restore stands): %v", item.Name, err)
				dumpReason = "the database did not become ready"
			} else if err := h.restoreDatabase(ctx, created.ID, item.Name, inspect.Config.Image, inspect.Config.Env, pendingDump); err != nil {
				// Fatal, per the invariant above. The reload has already
				// begun executing the dump's DROP/replace statements against
				// the volume-restored server, so the file-level result is no
				// longer an intact fallback to "stand on". A timeout, SQL
				// error, or full disk part-way through leaves tables dropped
				// and only partly recreated; returning nil here would report
				// that as a completed restore.
				return fmt.Errorf("reloading database dump for %s: %w (the database is partially reloaded — "+
					"its contents are not the restored state and should not be relied on)", item.Name, err)
			} else {
				// Reloaded successfully; nothing left to preserve.
				pendingDump = ""
			}
		}
	}

	// Step 8 (#289): a dump that was not reloaded — because the restored volume
	// is the newer, consistent copy, the container was not running, or the
	// server never became ready — would otherwise disappear with the staging
	// directory. Copy it somewhere durable and report the final path so the
	// operator can locate it (and replay it manually if they choose).
	if pendingDump != "" {
		if saved, err := preserveDatabaseDump(ctx, pendingDump, restoreDest, inspect); err != nil {
			log.Printf("engine: %s: WARNING could not preserve database dump %s: %v", item.Name, pendingDump, err)
		} else if saved != "" {
			progress(item.Name, 97, "database dump saved to "+saved)
			log.Printf("engine: %s: database dump not reloaded (%s) — saved to %s for manual replay", item.Name, dumpReason, saved)
		} else {
			log.Printf("engine: %s: database dump not reloaded (%s) and no durable restore location was available; it remains only in the temporary staging directory", item.Name, dumpReason)
		}
	}
	return nil
}

// preserveDatabaseDump copies a database dump that will not be auto-reloaded
// out of the temporary restore staging directory into a persistent, operator-
// discoverable location, returning the final path (or "" if there is nowhere
// durable to place it). The dump is a sidecar of the backup, not part of any
// restored volume, so without this it would vanish with the staging directory
// once the restore completes (#289).
func preserveDatabaseDump(ctx context.Context, dumpPath, restoreDest string, inspect restoreInspect) (string, error) {
	base := restoreDest
	if base == "" {
		// In-place restore: no custom destination was given, so land the dump
		// beside the container's first restored directory mount, which persists
		// on the host after the staging directory is cleaned up. File mounts
		// are skipped: their source is a single bind-mounted file, so dropping
		// the dump beside it would be surprising and could land it in an
		// unrelated directory.
		for _, m := range inspect.Mounts {
			if !backupableMount(m.Type) || m.Source == "" {
				continue
			}
			if info, err := os.Stat(m.Source); err != nil || !info.IsDir() {
				continue
			}
			base = filepath.Dir(m.Source)
			break
		}
	}
	if base == "" {
		return "", nil
	}
	// Prefix with the container name so restores that share a parent directory
	// (e.g. several appdata containers under /mnt/user/appdata) do not collide
	// on a bare "database.sql". base originates from a user-supplied restore
	// destination (or an inspected mount source), so confirm it resolves within
	// the approved restore roots before creating it; copyFile applies the same
	// guard to the file path itself.
	safeBase, err := normalizeRestorePath(base)
	if err != nil {
		return "", fmt.Errorf("resolving dump destination: %w", err)
	}
	if err := os.MkdirAll(safeBase, 0750); err != nil {
		return "", fmt.Errorf("creating dump destination %s: %w", safeBase, err)
	}
	dest := filepath.Join(base, restoreDumpName(inspect.Name, filepath.Base(dumpPath)))
	if err := copyFile(ctx, dumpPath, dest); err != nil {
		return "", fmt.Errorf("copying database dump to %s: %w", dest, err)
	}
	return dest, nil
}

// restoreDumpName builds a collision-resistant file name for a preserved dump,
// prefixing the container name (sanitised to a single path component) onto the
// dump's base name. Falls back to the bare dump name when the container name
// yields nothing usable.
func restoreDumpName(containerName, dumpBase string) string {
	clean := filepath.Base(strings.TrimPrefix(containerName, "/"))
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return dumpBase
	}
	return clean + "-" + dumpBase
}

// findDatabaseDump locates the dump file a backup may carry, whatever
// compression it was written with.
func findDatabaseDump(sourceDir string) string {
	for _, ext := range []string{"", ".gz", ".zst"} {
		candidate := filepath.Join(sourceDir, DatabaseDumpFile+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// Special manifest-key prefixes used by the dedup-chunked container backup
// format. The Manifest.Files map mixes these synthetic keys (always prefixed
// "__") with… nothing else: containers only ever store synthetic entries,
// never per-file entries (per-file chunking happens inside the nested
// FolderHandler manifests pointed to by the __vol__ entries). Document the
// shape here so future readers don't have to reverse-engineer it.
//
//	__inspect            → one-chunk entry holding json.Marshal(container.InspectResponse)
//	__image_meta         → one-chunk entry holding imageMeta JSON (best-effort)
//	__vol__<destination> → one-chunk entry whose single chunk ID is the
//	                       sub-manifest ID of a nested FolderHandler.BackupChunked
//	                       for that bind mount's source path. Skipped volumes are
//	                       represented with Size: -1 and an empty Chunks slice,
//	                       so the volume manifest is preserved for diagnostics
//	                       even when we didn't back it up.
//	__dbdump__           → chunked logical database dump, written only when the
//	                       item enables database_dump (see container_db.go)
//	__dbdump_replay__    → zero-value marker entry: present when the dump should
//	                       be replayed into the live server on restore
const (
	containerInspectKey   = "__inspect"
	containerImageMetaKey = "__image_meta"
	containerVolPrefix    = "__vol__"
	// volumeSkippedSize is the sentinel size stored on a __vol__<dest> entry
	// when shouldSkipVolume returned true at backup time. Restore uses this
	// to skip the entry without trying to dereference a missing chunk ID.
	volumeSkippedSize int64 = -1
)

// BackupChunked is the dedup-repo equivalent of Backup. Scope is
// volumes-only: each BIND mount's source tree is delegated to
// FolderHandler.BackupChunked (so chunk-level dedup is shared with the
// folder backup type), and the container's inspect JSON + image metadata
// are stored as single-chunk entries on the top-level manifest.
//
// Image bytes are intentionally NOT preserved — docker layer streams are
// already compressed and dedup poorly, while volumes (Unraid appdata) are
// where real dedup value lives. RestoreChunked runs `docker pull` for the
// recorded image tag, mirroring how the classic restore uses ImageLoad +
// post-pull RepoDigests refresh.
//
// Like FolderHandler.BackupChunked, repo.Flush is NOT called here — the
// runner flushes once per backup run after all items complete.
func (h *ContainerHandler) BackupChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, parent *dedup.Manifest, progress ProgressFunc) (dedup.ID, error) {
	if repo == nil {
		return dedup.ID{}, fmt.Errorf("container: dedup repo is nil")
	}

	// Resolve container ID first, then inspect (mirrors classic Backup's
	// fallback: stored ID may be stale, so re-resolve by name).
	containerID, _ := item.Settings["id"].(string)
	if containerID == "" {
		containerID = item.Name
	}
	if progress != nil {
		progress(item.Name, 10, "inspecting container")
	}
	inspectResult, err := h.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		inspectResult, err = h.cli.ContainerInspect(ctx, item.Name, client.ContainerInspectOptions{})
		if err != nil {
			return dedup.ID{}, fmt.Errorf("inspecting container: %w", err)
		}
		// Reassign containerID from the fresh inspect so that
		// ContainerStop/ContainerStart below target the current container,
		// not a stale ID from a previous run. Mirrors the classic Backup
		// path (see containerID reassignment after fallback inspect).
		containerID = inspectResult.Container.ID
		log.Printf("[backup-chunked] container %q: resolved by name (ID changed from stored value to %s)", item.Name, ShortID(containerID))
	}
	inspect := inspectResult.Container
	warnNetworkDependency(inspect, item.Name)

	m := dedup.Manifest{
		Version: dedup.ManifestVersion,
		Item:    item.Name,
		Files:   map[string]dedup.ManifestEntry{},
	}

	// 1. Inspect JSON — used by RestoreChunked to recreate the container.
	inspectBody, err := json.Marshal(inspect)
	if err != nil {
		return dedup.ID{}, fmt.Errorf("marshal inspect: %w", err)
	}
	inspectChunkID, err := repo.Put(inspectBody)
	if err != nil {
		return dedup.ID{}, fmt.Errorf("put inspect: %w", err)
	}
	m.Files[containerInspectKey] = dedup.ManifestEntry{
		Size:   int64(len(inspectBody)),
		Chunks: []dedup.ID{inspectChunkID},
	}

	// 2. Image metadata (best-effort). Used by restoreUnraidUpdateStatus
	//    to repopulate Unraid's docker update-status.json after the post-
	//    restore `docker pull` runs. Failures here are logged inside
	//    buildImageMeta; if RepoDigests come back empty we still write the
	//    entry so the restore path knows the meta was captured.
	if inspect.Config != nil && inspect.Config.Image != "" {
		meta := buildImageMeta(ctx, h.cli, inspect.Config.Image)
		if metaBody, mErr := json.MarshalIndent(meta, "", "  "); mErr == nil {
			metaChunkID, mPutErr := repo.Put(metaBody)
			if mPutErr != nil {
				return dedup.ID{}, fmt.Errorf("put image_meta: %w", mPutErr)
			}
			m.Files[containerImageMetaKey] = dedup.ManifestEntry{
				Size:   int64(len(metaBody)),
				Chunks: []dedup.ID{metaChunkID},
			}
		}
	}

	// 3. Bind mounts and named volumes — each delegated to
	//    FolderHandler.BackupChunked. Non-data mounts (tmpfs, npipe, etc.) are
	//    skipped to match the classic Backup path. Skipped mounts (per
	//    shouldSkipVolume) are recorded with Size: volumeSkippedSize so
	//    RestoreChunked can keep the diagnostic entry but not dereference
	//    a missing sub-manifest.
	fh := &FolderHandler{}
	// User exclusion patterns from the job item, applied identically to the
	// classic tar Backup path: whole excluded mounts are skipped
	// (shouldExcludeMount) and surviving mounts get their patterns mapped to
	// volume-relative paths (mapExclusionsToVolume) for the chunked walk.
	exclusions := containerExclusionsWithLabels(item.Settings, containerLabels(inspect))
	// Same all-mounts-excluded guard the classic path applies (see the
	// countExcludedMounts call in Backup). The chunked path records each excluded
	// mount as skipped and would otherwise commit a manifest holding no volume
	// data at all — a restore point that cannot reconstruct the container, which
	// retention may later keep in place of a complete one. Checked before the
	// stop so a doomed run never takes the container down.
	if eligible, excluded := countExcludedMounts(inspect.Mounts, exclusions); eligible > 0 && excluded == eligible {
		return dedup.ID{}, fmt.Errorf("every backup-eligible mount of container %s is excluded (%d of %d); "+
			"check the job's exclusions, the global exclusion list, and any %s label on the container",
			item.Name, excluded, eligible, VaultExcludeLabel)
	}
	changedSince, hasChangedSince := parseChangedSince(item.Settings)
	// Stop container for consistent backup (mirrors classic Backup path).
	// Differential runs: when no backupable volume changed since the parent
	// restore point, skip the stop/restart cycle entirely — the in-loop
	// per-volume pathChangedSince checks skip every volume anyway. The
	// chunked path saves no image.tar (restore pulls the image), so only
	// volume changes gate the stop.
	wasRunning := inspect.State.Running
	noStop, _ := item.Settings["no_stop"].(bool)

	// Logical database dump BEFORE the stop, for the same reason as the classic
	// path: the dump talks to the live server. Chunked into the repo rather
	// than compressed to a file, so successive dumps deduplicate against each
	// other instead of each one landing as an entirely new blob.
	if databaseDumpEnabled(item.Settings) && inspect.Config != nil {
		if !wasRunning || inspect.State.Restarting {
			return dedup.ID{}, fmt.Errorf("database dump requested for %s but the container is %s; "+
				"a dump needs the server running — use the one-by-one container mode, or start the container",
				item.Name, containerStateDescription(string(inspect.State.Status), inspect.State.Restarting))
		} else {
			dumpPath, cleanupDump, dErr := h.dumpDatabaseToTemp(ctx, containerID, item.Name, inspect.Config.Image, inspect.Config.Env)
			if dErr != nil {
				return dedup.ID{}, dErr
			}
			if dumpPath != "" {
				defer cleanupDump()
				entry, cErr := chunkFileIntoRepo(repo, dumpPath)
				if cErr != nil {
					return dedup.ID{}, fmt.Errorf("chunking database dump for %s: %w", item.Name, cErr)
				}
				m.Files[ContainerDBDumpKey] = entry
			}
		}
	}

	needsStop := wasRunning && !noStop
	var volChanges map[string]bool
	if needsStop && hasChangedSince {
		// Accepted race: if a file changes between this pre-check and the
		// archiving loop, that volume is backed up live instead of stopped.
		//
		// Per-volume results are cached so the archiving loop reuses them.
		var anyChanged bool
		var err error
		// Derive the per-volume previous listing from the parent manifest's
		// volume sub-manifests (the same resolution the archiving loop uses
		// for carry-forward) so a NEW stale-mtime file flips the volume to
		// changed (issue #320) — matching the classic Backup path's
		// prev-aware stop check. Without it, a running container whose only
		// change is such a file is not stopped and is backed up live.
		prevBySource := chunkedPrevBySource(parent, inspect.Mounts, repo)
		volChanges, anyChanged, err = anyVolumeChangedSince(ctx, inspect.Mounts, exclusions, changedSince, prevBySource)
		if err != nil {
			return dedup.ID{}, err
		}
		if !anyChanged {
			log.Printf("engine: chunked: container %s volumes unchanged since %s — skipping stop/restart",
				item.Name, changedSince.Format(time.RFC3339))
			needsStop = false
		}
	}

	// needsStop is final here — see the matching comment on the classic path.
	if _, hasDump := m.Files[ContainerDBDumpKey]; hasDump && wasRunning && !needsStop {
		m.Files[ContainerDBReplayKey] = dedup.ManifestEntry{}
	}
	if needsStop {
		if progress != nil {
			progress(item.Name, 20, "stopping container")
		}
		if _, err := h.cli.ContainerStop(ctx, containerID, client.ContainerStopOptions{}); err != nil {
			return dedup.ID{}, fmt.Errorf("stopping container %s: %w", item.Name, err)
		}
	}
	if progress != nil {
		progress(item.Name, 60, "backing up volumes")
	}

	err = runWithRestart(needsStop, item.Name, progress, func() error {
		for _, mnt := range inspect.Mounts {
			if !backupableMount(string(mnt.Type)) {
				continue
			}
			if mnt.Source == "" || mnt.Destination == "" {
				continue
			}
			key := containerVolPrefix + mnt.Destination
			if !restorableVolume(string(mnt.Type), mnt.Name) {
				log.Printf("engine: chunked: skipping anonymous volume %q for %s (not restorable)", mnt.Destination, item.Name)
				m.Files[key] = dedup.ManifestEntry{Size: volumeSkippedSize}
				continue
			}
			if skip, reason := shouldSkipVolume(mnt.Source); skip {
				log.Printf("engine: chunked: skipping volume %s for %s: %s", mnt.Source, item.Name, reason)
				m.Files[key] = dedup.ManifestEntry{Size: volumeSkippedSize}
				continue
			}
			// Honour exclusion patterns at the volume level — for a `/` → `/rootfs`
			// bind mount (Glances, Telegraf, Netdata) this prevents walking the
			// entire host filesystem. Recorded as skipped so RestoreChunked keeps
			// the diagnostic entry without dereferencing a missing sub-manifest.
			if shouldExcludeMount(exclusions, mnt.Destination) {
				log.Printf("engine: chunked: skipping volume %s for %s: matches exclusion pattern", mnt.Source, item.Name)
				m.Files[key] = dedup.ManifestEntry{Size: volumeSkippedSize}
				continue
			}
			// For differential backups, skip volumes whose entire tree is
			// unchanged since the reference time. Mirrors the classic Backup
			// path. Reuses cached pre-check results when available. With a
			// parent manifest, the unchanged volume's sub-manifest reference is
			// carried forward so the resulting manifest stays complete for
			// single-point restore (issue #320).
			// Resolve the parent's matching sub-manifest up front so the
			// unchanged-volume short-circuit below can route through it
			// (per-file carry-forward with the stale-mtime fall-through)
			// instead of copying the parent entry verbatim — the latter
			// silently drops a NEW file whose mtime predates changed_since
			// (e.g. cp -a), the literal issue #320 data-loss class. The
			// len(pe.Chunks) > 0 guard also rejects a pre-PR parent entry
			// that recorded the volume as the -1 skip sentinel, which must
			// be re-chunked rather than carried forward.
			var volParent *dedup.Manifest
			if hasChangedSince && parent != nil {
				if pe, ok := parent.Files[key]; ok && len(pe.Chunks) > 0 {
					if sub, gErr := repo.GetManifest(pe.Chunks[0]); gErr == nil {
						volParent = &sub
					}
				}
			}

			if hasChangedSince {
				changed, cached := volChanges[mnt.Source]
				if !cached {
					var err error
					changed, err = pathChangedSince(ctx, mnt.Source, changedSince)
					if err != nil {
						return fmt.Errorf("checking volume %s changes: %w", mnt.Source, err)
					}
				}
				if !changed && volParent == nil {
					// The volume looks unchanged AND there is no parent
					// sub-manifest to carry forward from (a full backup with a
					// changed_since set, a parent whose manifest is missing
					// this volume, or a pre-PR parent that recorded it as the
					// -1 skip sentinel). Chunk FULLY so the manifest stays
					// complete. changed_since is deliberately NOT propagated
					// here — with no parent entry there is nothing to carry
					// forward from, and the per-file filter would otherwise
					// drop every file (issue #320).
					log.Printf("engine: chunked: volume %s for %s unchanged since reference — no parent entry to carry forward, chunking fully", mnt.Source, item.Name)
					volItem := BackupItem{
						Name: mnt.Destination,
						Type: "folder",
						Settings: map[string]any{
							"path":          mnt.Source,
							"exclude_paths": mapExclusionsToVolume(exclusions, mnt.Destination),
						},
					}
					volManifestID, vErr := fh.BackupChunked(ctx, volItem, repo, nil, progress)
					if vErr != nil {
						return fmt.Errorf("backup unchanged volume %s: %w", mnt.Destination, vErr)
					}
					m.Files[key] = dedup.ManifestEntry{
						Size:   0,
						Chunks: []dedup.ID{volManifestID},
					}
					continue
				}
				// A volume that is unchanged by mtime but still has a parent
				// sub-manifest falls through: it is re-chunked with volParent
				// below, which carries forward unchanged files AND chunks any
				// new stale-mtime file (issue #320).
			}
			volItem := BackupItem{
				Name: mnt.Destination,
				Type: "folder",
				Settings: map[string]any{
					"path":          mnt.Source,
					"exclude_paths": mapExclusionsToVolume(exclusions, mnt.Destination),
				},
			}
			if volParent != nil {
				volItem.Settings["changed_since"] = item.Settings["changed_since"]
			}
			volManifestID, vErr := fh.BackupChunked(ctx, volItem, repo, volParent, progress)
			if vErr != nil {
				return fmt.Errorf("backup volume %s: %w", mnt.Destination, vErr)
			}
			m.Files[key] = dedup.ManifestEntry{
				Size:   0,
				Chunks: []dedup.ID{volManifestID},
			}
		}
		return nil
	}, func() error {
		restartCtx, cancel := restartCleanupCtx(ctx)
		defer cancel()
		_, err := h.cli.ContainerStart(restartCtx, containerID, client.ContainerStartOptions{})
		return err
	})
	if err != nil {
		return dedup.ID{}, err
	}

	manifestID, err := repo.PutManifest(item.Name, m)
	if err != nil {
		return dedup.ID{}, err
	}
	if progress != nil {
		progress(item.Name, 100, fmt.Sprintf("manifest written (%d volumes)", len(m.Files)-1))
	}
	return manifestID, nil
}

// RestoreChunked is the dedup-repo equivalent of Restore. It decodes the
// stored inspect blob, runs `docker pull` for the recorded image tag (the
// chunked backup did NOT save image.tar — see BackupChunked's docstring),
// seeds Unraid's update-status cache from the captured image_meta blob,
// restores each volume tree via FolderHandler.RestoreChunked, and finally
// recreates + starts the container via the shared
// recreateAndStartContainer helper.
//
// The destPath argument is the custom restore destination (empty for
// in-place restore). When set, volumes are extracted under
// destPath/<volume-name> rather than their original bind sources, matching
// the bind-mount rewrite recreateAndStartContainer applies.
func (h *ContainerHandler) RestoreChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, manifestID dedup.ID, destPath string, progress ProgressFunc) error {
	if repo == nil {
		return fmt.Errorf("container: dedup repo is nil")
	}
	m, err := repo.GetManifest(manifestID)
	if err != nil {
		return err
	}

	// 1. Inspect blob — required.
	inspectEntry, ok := m.Files[containerInspectKey]
	if !ok || len(inspectEntry.Chunks) == 0 {
		return fmt.Errorf("container restore: manifest missing %s entry", containerInspectKey)
	}
	inspectBody, err := repo.Get(inspectEntry.Chunks[0])
	if err != nil {
		return fmt.Errorf("get inspect chunk: %w", err)
	}
	// Decode into the same partial-struct shape used by classic Restore —
	// both paths share recreateAndStartContainer below.
	var inspect restoreInspect
	if err := json.Unmarshal(inspectBody, &inspect); err != nil {
		return fmt.Errorf("parse inspect: %w", err)
	}

	// 2. Pull the image fresh. We didn't save image.tar in chunked scope,
	//    so this is the only way to get the layers locally. Errors are
	//    logged-and-continued because the user may want to restore the
	//    container metadata + volumes even when the registry is offline
	//    (the container will fail to start, but at least the data is
	//    back). populateRepoDigestsViaPull (called inside
	//    recreateAndStartContainer) will run a second pull to refresh
	//    RepoDigests for Unraid's update-status cache.
	if inspect.Config.Image != "" {
		if progress != nil {
			progress(item.Name, 20, "pulling image "+inspect.Config.Image)
		}
		pullResp, pullErr := h.cli.ImagePull(ctx, inspect.Config.Image, client.ImagePullOptions{})
		if pullErr != nil {
			log.Printf("engine: chunked restore: image pull %q: %v (continuing — container may fail to start)", inspect.Config.Image, pullErr)
		} else {
			if waitErr := pullResp.Wait(ctx); waitErr != nil {
				log.Printf("engine: chunked restore: image pull wait %q: %v", inspect.Config.Image, waitErr)
			}
			_ = pullResp.Close()
		}
	}

	// 3. Seed Unraid update-status from the captured image_meta (only used
	//    when the post-create populateRepoDigestsViaPull fails — e.g.
	//    offline registry). Reuses the existing seedUnraidUpdateStatus
	//    helper so the on-disk update-status.json format stays in sync
	//    with the classic path. (recreateAndStartContainer calls
	//    populateRepoDigestsViaPull again after the container is created.)
	if metaEntry, ok := m.Files[containerImageMetaKey]; ok && len(metaEntry.Chunks) > 0 {
		if metaBody, mErr := repo.Get(metaEntry.Chunks[0]); mErr == nil {
			var meta imageMeta
			if jErr := json.Unmarshal(metaBody, &meta); jErr == nil && len(meta.RepoDigests) > 0 {
				if sha := extractSHA(meta.RepoDigests[0]); sha != "" {
					seedUnraidUpdateStatus(inspect.Config.Image, sha)
				}
			}
		}
	}

	// Resolve custom restore destination: explicit destPath parameter wins,
	// fall back to item.Settings["restore_destination"].
	restoreDest := destPath
	if restoreDest == "" {
		restoreDest, _ = item.Settings["restore_destination"].(string)
	}
	if restoreDest != "" {
		normalized, err := normalizeRestorePath(restoreDest)
		if err != nil {
			return err
		}
		restoreDest = normalized
	}

	// 4. Restore volumes. Each __vol__<dest> entry's single chunk ID is a
	//    sub-manifest ID — restoreChunkedVolumes routes each to the correct
	//    target (original source or custom dest).
	if progress != nil {
		progress(item.Name, 40, "restoring volumes")
	}
	if err := restoreChunkedVolumes(ctx, m, repo, inspect, restoreDest, progress); err != nil {
		return err
	}

	// 5. Recreate + start container (shared helper with classic restore).
	//    Pass sourceDir="" because the chunked format has no template.xml
	//    or image_meta.json sidecars — image_meta seeding above already
	//    handled the update-status seeding from the manifest entry.
	// Reassemble the logical dump, if this backup carried one, so the shared
	// recreate path reloads it exactly as it does for a classic backup.
	// Without this a dedup restore would silently ignore a dump the backup
	// went to the trouble of taking.
	dumpDir, cleanupDump, err := writeChunkedDatabaseDump(repo, m)
	if err != nil {
		return err
	}
	if cleanupDump != nil {
		defer cleanupDump()
	}

	if err := h.recreateAndStartContainer(ctx, item, inspect, restoreDest, dumpDir, progress); err != nil {
		return err
	}
	if progress != nil {
		progress(item.Name, 100, "container restored")
	}
	return nil
}

// restoreChunkedVolumes restores each __vol__<dest> sub-manifest entry in m
// to its target directory: the original bind/volume source when restoreDest
// is empty, or restoreDest/<volume-name> when a custom destination is set
// (mirroring classic Restore and recreateAndStartContainer's bind rewrite).
// The function needs no Docker client — it delegates file extraction to
// FolderHandler.RestoreChunked — making it testable without a Docker mock.
func restoreChunkedVolumes(ctx context.Context, m dedup.Manifest, repo *dedup.Repo, inspect restoreInspect, restoreDest string, progress ProgressFunc) error {
	if err := checkVolumeTargetCollisions(restoreDest, inspect.mountInfos()); err != nil {
		return err
	}
	fh := &FolderHandler{}
	mountByDest := map[string]mountInfo{}
	for _, mnt := range inspect.Mounts {
		if backupableMount(mnt.Type) && mnt.Destination != "" {
			mountByDest[mnt.Destination] = mountInfo{
				Type:        mnt.Type,
				Name:        mnt.Name,
				Source:      mnt.Source,
				Destination: mnt.Destination,
			}
		}
	}
	for k, v := range m.Files {
		if !strings.HasPrefix(k, containerVolPrefix) {
			continue
		}
		if v.Size == volumeSkippedSize {
			log.Printf("engine: chunked restore: %s was skipped at backup time, nothing to restore", k)
			continue
		}
		if len(v.Chunks) == 0 {
			log.Printf("engine: chunked restore: %s has no chunks, skipping", k)
			continue
		}
		dest := strings.TrimPrefix(k, containerVolPrefix)
		mnt, ok := mountByDest[dest]
		if !ok || mnt.Source == "" {
			log.Printf("engine: chunked restore: no matching bind mount for %s in inspect — skipping", dest)
			continue
		}
		src, err := volumeRestoreTarget(restoreDest, mnt.Type, mnt.Name, mnt.Source)
		if err != nil {
			return fmt.Errorf("restore volume %s: %w", dest, err)
		}
		normalizedSrc, err := normalizeRestorePath(src)
		if err != nil {
			return fmt.Errorf("restore volume %s: %w", dest, err)
		}
		src = normalizedSrc
		if err := os.MkdirAll(src, 0o750); err != nil {
			return fmt.Errorf("mkdir volume %s: %w", src, err)
		}
		proxy := BackupItem{Name: dest, Type: "folder"}
		if err := fh.RestoreChunked(ctx, proxy, repo, v.Chunks[0], src, progress); err != nil {
			return fmt.Errorf("restore volume %s: %w", dest, err)
		}
	}
	return nil
}

// writeChunkedDatabaseDump materialises a manifest's database dump into a
// temporary directory shaped like a classic backup's source directory, so the
// shared restore path finds it with no special-casing. Returns an empty path
// when the manifest carries no dump.
func writeChunkedDatabaseDump(repo *dedup.Repo, m dedup.Manifest) (string, func(), error) {
	entry, ok := m.Files[ContainerDBDumpKey]
	if !ok || len(entry.Chunks) == 0 {
		return "", nil, nil
	}
	dir, err := os.MkdirTemp("", "vault-dbrestore-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating database dump directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// Carry the replay marker across into the classic-shaped directory so the
	// shared restore path applies the same rule on both.
	if _, replay := m.Files[ContainerDBReplayKey]; replay {
		if err := writeDatabaseReplayMarker(dir); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing database replay marker: %w", err)
		}
	}

	// Stored uncompressed by the chunked backup, so it is written back under
	// the bare name; findDatabaseDump accepts either form.
	path := filepath.Join(dir, DatabaseDumpFile)
	f, err := os.Create(path) // #nosec G304 — path is inside a vault-created temp directory
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("creating database dump file: %w", err)
	}
	for _, id := range entry.Chunks {
		chunk, err := repo.Get(id)
		if err != nil {
			_ = f.Close()
			cleanup()
			return "", nil, fmt.Errorf("reading database dump chunk: %w", err)
		}
		if _, err := f.Write(chunk); err != nil {
			_ = f.Close()
			cleanup()
			return "", nil, fmt.Errorf("writing database dump: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("closing database dump: %w", err)
	}
	return dir, cleanup, nil
}

// contextCopy copies from src to dst, checking for context cancellation
// periodically (every 32 KiB). This prevents a single large file from
// blocking cancellation indefinitely.
func contextCopy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if writeErr != nil {
				return written, writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
	}
}

// tarFile creates a tar archive (optionally compressed via compression) at
// destPath containing a single file from srcPath. Used for file-based bind
// mounts (e.g. Tailscale container hook).
func tarFile(ctx context.Context, srcPath, destPath, compression string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}

	outFile, err := os.Create(destPath) // #nosec G304 — destPath is destDir + fixed filename, caller-controlled
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	// The three closes below are data-critical: they flush the tar footer,
	// the compression trailer, and the final bytes to disk. A failure in any
	// of them (ENOSPC, short-written final entry) means a truncated archive,
	// which MUST fail the backup instead of being reported as success (#166).
	// LIFO defer order closes tar → compressor → file.
	defer func() {
		if cerr := outFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing archive file: %w", cerr)
		}
	}()

	cw, closeCompress, err := compressedWriter(outFile, compression)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeCompress(); cerr != nil && err == nil {
			err = fmt.Errorf("finalising compression: %w", cerr)
		}
	}()

	tw := tar.NewWriter(cw)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing tar writer: %w", cerr)
		}
	}()

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("creating tar header: %w", err)
	}
	header.Name = filepath.Base(srcPath)

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}

	f, err := os.Open(srcPath) // #nosec G304 — srcPath is bind-mount path from Docker inspect
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer f.Close()

	if _, err := contextCopy(ctx, tw, io.LimitReader(f, header.Size)); err != nil {
		return fmt.Errorf("writing file to tar: %w", err)
	}

	return nil
}

// untarFile extracts the first regular file from a tar archive and writes it
// to destPath. The archive may be plain, gzip-compressed, or zstd-compressed —
// the compression layer is auto-detected from the leading magic bytes. Used
// for restoring file-based bind mounts.
func untarFile(ctx context.Context, srcPath, destPath string) error {
	inFile, err := os.Open(srcPath) // #nosec G304 — srcPath is sourceDir + fixed volume archive name
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer inFile.Close()

	dr, closeDecompress, err := detectingReader(inFile)
	if err != nil {
		return err
	}
	defer func() { _ = closeDecompress() }()

	tr := tar.NewReader(dr)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no regular file found in archive")
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size < 0 {
			return fmt.Errorf("invalid file size %d for %s", header.Size, header.Name)
		}
		if header.Size > maxExtractSize {
			return fmt.Errorf("file %s exceeds max extract size (%d > %d)", header.Name, header.Size, maxExtractSize)
		}

		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeFileMode(header.Mode)) // #nosec G304 — destPath is restore destination from Docker container config
		if err != nil {
			return fmt.Errorf("creating file %s: %w", destPath, err)
		}
		n, err := contextCopy(ctx, f, io.LimitReader(tr, header.Size))
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("writing file %s: %w", destPath, err)
		}
		if n != header.Size {
			_ = f.Close()
			return fmt.Errorf("writing file %s: expected %d bytes, wrote %d", destPath, header.Size, n)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("closing file %s: %w", destPath, err)
		}
		return nil
	}
}

// tarDirectory creates a tar archive of srcDir at destPath. The compression
// argument selects the archive compression layer ("none", "gzip", or "zstd").
func tarDirectory(ctx context.Context, srcDir, destPath string, exclusions []string, compression string) (err error) {
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("opening source root: %w", err)
	}
	defer root.Close()

	outFile, err := os.Create(destPath) // #nosec G304 — destPath is destDir + fixed archive name, caller-controlled
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	// Data-critical closes: tar footer, compression trailer, final flush to
	// disk. Any failure means a truncated archive and MUST fail the backup
	// (#166) — tar.Writer.Close also surfaces "missed writing N bytes" when
	// the last file shrank between stat and copy. LIFO order: tar → compressor
	// → file.
	defer func() {
		if cerr := outFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing archive file: %w", cerr)
		}
	}()

	cw, closeCompress, err := compressedWriter(outFile, compression)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeCompress(); cerr != nil && err == nil {
			err = fmt.Errorf("finalising compression: %w", cerr)
		}
	}()

	tw := tar.NewWriter(cw)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing tar writer: %w", cerr)
		}
	}()

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if err != nil {
			// Skip broken symlinks and inaccessible files instead of
			// aborting the entire backup.
			log.Printf("engine: skipping inaccessible path %s: %v", path, err)
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Check exclusions before processing the entry.
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Handle symlinks: read the link target and store as symlink entry.
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				log.Printf("engine: skipping unreadable symlink %s: %v", rel, err)
				return nil
			}
			header := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     rel,
				Linkname: link,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(header)
		}

		// Skip special file types that tar cannot archive (sockets, devices, pipes).
		if info.Mode()&(os.ModeSocket|os.ModeCharDevice|os.ModeDevice|os.ModeNamedPipe) != 0 {
			log.Printf("engine: skipping special file %s (mode %s)", rel, info.Mode().String())
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", rel, err)
		}
		header.Name = rel

		if info.IsDir() {
			return tw.WriteHeader(header)
		}

		// Re-stat to get current size (file may have grown since Walk).
		currentInfo, err := os.Stat(path)
		if err != nil {
			log.Printf("engine: skipping vanished file %s: %v", rel, err)
			return nil
		}
		header.Size = currentInfo.Size()
		warnIfSparse(rel, currentInfo)

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", rel, err)
		}

		f, err := root.Open(rel)
		if err != nil {
			log.Printf("engine: skipping unopenable file %s: %v", rel, err)
			return nil
		}
		defer f.Close()

		// Use LimitReader to avoid "write too long" if the file grows
		// between stat and copy.
		if _, err := contextCopy(ctx, tw, io.LimitReader(f, header.Size)); err != nil {
			return fmt.Errorf("writing file %s to tar: %w", rel, err)
		}
		return nil
	})
	return err
}

// tarDirectoryFilteredWithPrev creates a tar archive of srcDir at destPath
// including only files whose modification time is after changedSince (directory
// entries are always included to preserve structure), plus a prevPaths set of
// item-relative paths recorded in the previous backup's effective listing. A
// regular file whose mtime predates changedSince is normally skipped as
// unchanged; when prevPaths is non-nil, a file ABSENT from the set (a NEW file
// copied in with a stale timestamp) is archived instead of dropped (issue #320).
// A nil prevPaths preserves the mtime-only behaviour.
func tarDirectoryFilteredWithPrev(ctx context.Context, srcDir, destPath string, changedSince time.Time, exclusions []string, compression string, prevPaths map[string]struct{}) (err error) {
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("opening source root: %w", err)
	}
	defer root.Close()

	outFile, err := os.Create(destPath) // #nosec G304 — destPath is destDir + fixed archive name, caller-controlled
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	// Data-critical closes — see tarDirectory (#166). LIFO order: tar →
	// compressor → file.
	defer func() {
		if cerr := outFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing archive file: %w", cerr)
		}
	}()

	cw, closeCompress, err := compressedWriter(outFile, compression)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeCompress(); cerr != nil && err == nil {
			err = fmt.Errorf("finalising compression: %w", cerr)
		}
	}()

	tw := tar.NewWriter(cw)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing tar writer: %w", cerr)
		}
	}()

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if err != nil {
			log.Printf("engine: skipping inaccessible path %s: %v", path, err)
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Check exclusions before processing the entry.
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Handle symlinks.
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				log.Printf("engine: skipping unreadable symlink %s: %v", rel, err)
				return nil
			}
			header := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     rel,
				Linkname: link,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(header)
		}

		// Skip special file types that tar cannot archive (sockets, devices, pipes).
		if info.Mode()&(os.ModeSocket|os.ModeCharDevice|os.ModeDevice|os.ModeNamedPipe) != 0 {
			log.Printf("engine: skipping special file %s (mode %s)", rel, info.Mode().String())
			return nil
		}

		// Always include directories; filter regular files by mod time. A file
		// whose mtime predates changedSince is unchanged — unless it is absent
		// from the previous backup's listing, in which case it is a NEW file
		// with a stale timestamp and must be archived (issue #320).
		if !info.IsDir() && !info.ModTime().After(changedSince) {
			if prevPaths == nil {
				return nil
			}
			if _, exists := prevPaths[rel]; exists {
				return nil
			}
			// Fall through and archive the new file.
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", rel, err)
		}
		header.Name = rel

		if info.IsDir() {
			return tw.WriteHeader(header)
		}

		// Re-stat to get current size.
		currentInfo, err := os.Stat(path)
		if err != nil {
			log.Printf("engine: skipping vanished file %s: %v", rel, err)
			return nil
		}
		header.Size = currentInfo.Size()

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", rel, err)
		}

		f, err := root.Open(rel)
		if err != nil {
			log.Printf("engine: skipping unopenable file %s: %v", rel, err)
			return nil
		}
		defer f.Close()

		if _, err := contextCopy(ctx, tw, io.LimitReader(f, header.Size)); err != nil {
			return fmt.Errorf("writing file %s to tar: %w", rel, err)
		}
		return nil
	})
	return err
}

// untarDirectory extracts a tar archive from srcPath into destDir. The archive
// may be plain, gzip-compressed, or zstd-compressed — the compression layer
// is auto-detected from the leading magic bytes so legacy and new archives
// both restore correctly.
func untarDirectory(ctx context.Context, srcPath, destDir string) error {
	return untarDirectoryFiltered(ctx, srcPath, destDir, nil)
}

// dirModTime records a directory target path and its archive modification time
// so mtimes can be applied in reverse insertion order after all child entries
// are written.
type dirModTime struct {
	path    string
	modTime time.Time
}

// untarDirectoryFiltered behaves like untarDirectory but only extracts the
// tar entries whose Name is present in the include set. Any entry whose path
// is the descendant of an included directory is also extracted. The empty
// set restores every entry (caller passes nil for "extract everything").
//
// This is the v1 partial-restore path: callers supply file paths chosen
// from the engine's tar index sidecar (see WriteTarIndex). Compatible with
// every archive — encrypted/compressed/plain — because filtering happens
// after the existing decryption + decompression pipeline that the runner
// has already staged.
func untarDirectoryFiltered(ctx context.Context, srcPath, destDir string, include []string) error {
	includeSet := newIncludeSet(include)
	inFile, err := os.Open(srcPath) // #nosec G304 — srcPath is sourceDir + fixed archive name, caller-controlled
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer inFile.Close()

	dr, closeDecompress, err := detectingReader(inFile)
	if err != nil {
		return err
	}
	defer func() { _ = closeDecompress() }()

	var dirModTimes []dirModTime
	tr := tar.NewReader(dr)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		if !includeSet.matches(header.Name) {
			continue
		}

		target, err := joinArchiveTarget(destDir, header.Name)
		if err != nil {
			return fmt.Errorf("illegal file path in archive: %s: %w", header.Name, err)
		}

		// Resolve previously-extracted symlinks to prevent path escape (CWE-22).
		if err := resolveWithinBase(destDir, target); err != nil {
			return fmt.Errorf("path escape in archive entry %s: %w", header.Name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, safeFileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
			dirModTimes = append(dirModTimes, dirModTime{path: target, modTime: header.ModTime})
		case tar.TypeReg:
			if header.Size < 0 {
				return fmt.Errorf("invalid file size %d for %s", header.Size, header.Name)
			}
			if header.Size > maxExtractSize {
				return fmt.Errorf("file %s exceeds max extract size (%d > %d)", header.Name, header.Size, maxExtractSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeFileMode(header.Mode)) // #nosec G304 — target validated by joinArchiveTarget + resolveWithinBase (Zip Slip protected)
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}
			n, err := contextCopy(ctx, f, io.LimitReader(tr, header.Size))
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("writing file %s: %w", target, err)
			}
			if n != header.Size {
				_ = f.Close()
				return fmt.Errorf("writing file %s: expected %d bytes, wrote %d", target, header.Size, n)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("closing file %s: %w", target, err)
			}
			_ = os.Chtimes(target, header.ModTime, header.ModTime)
		case tar.TypeSymlink:
			// Validate symlink target resolves within destDir after following existing symlinks.
			if err := resolveSymlinkTarget(destDir, target, header.Linkname); err != nil {
				return fmt.Errorf("unsafe symlink in archive: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			removeExistingNonDir(target)                                // overwrite semantics for links (#175)
			if err := os.Symlink(header.Linkname, target); err != nil { // #nosec G305 — target validated by joinArchiveTarget, linkname validated by resolveSymlinkTarget
				return fmt.Errorf("creating symlink %s -> %s: %w", target, header.Linkname, err)
			}
		case tar.TypeLink:
			linkTarget, err := joinArchiveTarget(destDir, header.Linkname)
			if err != nil {
				return fmt.Errorf("illegal hard link target in archive: %s: %w", header.Linkname, err)
			}
			if err := resolveWithinBase(destDir, linkTarget); err != nil {
				return fmt.Errorf("path escape in hard link target %s: %w", header.Linkname, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			removeExistingNonDir(target) // overwrite semantics for links (#175)
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("creating hard link %s -> %s: %w", target, linkTarget, err)
			}
			_ = os.Chtimes(target, header.ModTime, header.ModTime)
		default:
			continue
		}
	}
	for i := len(dirModTimes) - 1; i >= 0; i-- {
		_ = os.Chtimes(dirModTimes[i].path, dirModTimes[i].modTime, dirModTimes[i].modTime)
	}
	return nil
}

// removeExistingNonDir removes an existing file or link at target so a
// symlink/hardlink tar entry can be recreated over it — regular files
// already get overwrite semantics via O_TRUNC, but os.Symlink/os.Link fail
// with EEXIST, which aborted restores over an existing installation at the
// first pre-existing link (issue #175). Directories are never removed: a
// dir-vs-link conflict is surfaced by the subsequent create call instead.
func removeExistingNonDir(target string) {
	if fi, err := os.Lstat(target); err == nil && !fi.IsDir() {
		_ = os.Remove(target)
	}
}

// StopContainers stops the given container IDs in order. It returns the IDs
// that were actually stopped (i.e. were running) so the caller can restart them.
func StopContainers(ids []string) ([]string, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	var stopped []string
	for _, id := range ids {
		result, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return stopped, fmt.Errorf("inspecting container %s: %w", id, err)
		}
		if !result.Container.State.Running {
			continue
		}
		if _, err := cli.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
			return stopped, fmt.Errorf("stopping container %s: %w", id, err)
		}
		stopped = append(stopped, id)
	}
	return stopped, nil
}

// StartContainers starts the given container IDs. Errors are logged but do not
// abort the remaining starts so that as many containers as possible are restored.
func StartContainers(ids []string) []error {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return []error{fmt.Errorf("creating docker client: %w", err)}
	}
	defer cli.Close()

	ctx := context.Background()
	var errs []error
	for _, id := range ids {
		if _, err := cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("starting container %s: %w", id, err))
		}
	}
	return errs
}

// HealthCheckResult describes the post-restart health of a container.
type HealthCheckResult struct {
	ContainerName string        `json:"container_name"`
	Status        string        `json:"status"` // "healthy", "running", "unhealthy", "failed"
	Duration      time.Duration `json:"duration_ms"`
	Message       string        `json:"message"`
}

// VerifyContainerHealth polls a container's state after restart to determine
// if it is healthy. It checks Docker HEALTHCHECK status, running state, and
// optionally exposed port connectivity. Timeout is per-container.
func VerifyContainerHealth(containerID, containerName string, timeout time.Duration) (*HealthCheckResult, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &HealthCheckResult{
				ContainerName: containerName,
				Status:        "failed",
				Duration:      time.Since(start),
				Message:       "Timed out waiting for healthy state",
			}, nil
		case <-ticker.C:
			result, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
			if err != nil {
				continue
			}

			state := result.Container.State
			if state == nil {
				continue
			}

			// Container not running at all — immediate failure.
			if !state.Running {
				if state.Restarting {
					continue // still restarting, keep polling
				}
				return &HealthCheckResult{
					ContainerName: containerName,
					Status:        "failed",
					Duration:      time.Since(start),
					Message:       fmt.Sprintf("Container is %s (exit code %d)", state.Status, state.ExitCode),
				}, nil
			}

			// If container defines a HEALTHCHECK, wait for it.
			if state.Health != nil {
				switch state.Health.Status {
				case "healthy":
					return &HealthCheckResult{
						ContainerName: containerName,
						Status:        "healthy",
						Duration:      time.Since(start),
						Message:       "Docker HEALTHCHECK passed",
					}, nil
				case "unhealthy":
					return &HealthCheckResult{
						ContainerName: containerName,
						Status:        "unhealthy",
						Duration:      time.Since(start),
						Message:       "Docker HEALTHCHECK reports unhealthy",
					}, nil
				default:
					continue // "starting" — keep polling
				}
			}

			// No HEALTHCHECK defined — "running" is good enough.
			return &HealthCheckResult{
				ContainerName: containerName,
				Status:        "running",
				Duration:      time.Since(start),
				Message:       "Container is running (no HEALTHCHECK defined)",
			}, nil
		}
	}
}

// ShortID returns the first 12 characters of a Docker container ID for log
// lines, or the input unchanged when it is shorter. Stored item IDs can be
// short or empty (imported jobs record no container ID), so an unguarded
// [:12] slice panicked the run (issue #170).
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	if id == "" {
		return "<none>"
	}
	return id
}

// backupFileInfo returns a BackupFile with name and size from the given path.
func backupFileInfo(path string) BackupFile {
	info, err := os.Stat(path)
	if err != nil {
		return BackupFile{Name: filepath.Base(path)}
	}
	return BackupFile{Name: filepath.Base(path), Size: info.Size()}
}

// parseDNSAddrs converts DNS server strings from a saved config to netip.Addr
// values expected by the Docker API. Invalid entries are silently skipped.
func parseDNSAddrs(servers []string) []netip.Addr {
	if len(servers) == 0 {
		return nil
	}
	addrs := make([]netip.Addr, 0, len(servers))
	for _, s := range servers {
		if addr, err := netip.ParseAddr(s); err == nil {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// ProbeActivity measures a container's CPU and network activity over a short
// interval (issue #240 adaptive backups). Two one-shot stats samples ~1s
// apart: CPU%% from the second sample's cpu/precpu delta, network rate from
// the cumulative counter difference. Any failure returns an unknown sample
// (fail-open — callers treat unknown as idle).
func (h *ContainerHandler) ProbeActivity(ctx context.Context, containerID string) ActivitySample {
	first, err := h.readStatsSample(ctx, containerID)
	if err != nil {
		return ActivitySample{}
	}
	select {
	case <-ctx.Done():
		return ActivitySample{}
	case <-time.After(1 * time.Second):
	}
	second, err := h.readStatsSample(ctx, containerID)
	if err != nil {
		return ActivitySample{}
	}

	sample := ActivitySample{Known: true}

	// Docker CPU%% formula computed across OUR two samples — one-shot
	// responses may carry empty precpu counters depending on daemon/client
	// version, so never trust PreCPUStats.
	cpuDelta := float64(second.CPUStats.CPUUsage.TotalUsage) - float64(first.CPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(second.CPUStats.SystemUsage) - float64(first.CPUStats.SystemUsage)
	onlineCPUs := float64(second.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(second.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta > 0 && sysDelta > 0 && onlineCPUs > 0 {
		sample.CPUPercent = cpuDelta / sysDelta * onlineCPUs * 100
	}

	sumNet := func(s *container.StatsResponse) (total uint64) {
		for _, n := range s.Networks {
			total += n.RxBytes + n.TxBytes
		}
		return total
	}
	interval := second.Read.Sub(first.Read).Seconds()
	if interval <= 0 {
		interval = 1
	}
	if n2, n1 := sumNet(second), sumNet(first); n2 >= n1 {
		sample.NetBytesPerSec = float64(n2-n1) / interval
	}
	return sample
}

func (h *ContainerHandler) readStatsSample(ctx context.Context, containerID string) (*container.StatsResponse, error) {
	res, err := h.cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{Stream: false})
	if err != nil {
		return nil, fmt.Errorf("container stats for %s: %w", containerID, err)
	}
	defer res.Body.Close()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding stats for %s: %w", containerID, err)
	}
	return &s, nil
}
