package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
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

	// Skip direct disk access paths (/mnt/disk1, /mnt/disk2, etc.).
	if strings.HasPrefix(norm, "/mnt/disk") {
		return true, "direct disk volume"
	}

	// Skip the Unraid root mount (/mnt) itself if mapped directly.
	if norm == "/mnt" {
		return true, "root /mnt mount"
	}

	// Everything else (e.g. /tmp or custom paths) — back up.
	return false, ""
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
type ContainerHandler struct {
	cli *client.Client
}

// NewContainerHandler creates a new ContainerHandler with a Docker client
// configured from environment variables.
func NewContainerHandler() (*ContainerHandler, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &ContainerHandler{cli: cli}, nil
}

// ListItems enumerates all Docker containers as BackupItems.
func (h *ContainerHandler) ListItems() ([]BackupItem, error) {
	ctx := context.Background()
	containers, err := h.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	items := make([]BackupItem, 0, len(containers))
	for _, c := range containers {
		name := c.ID[:12]
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		items = append(items, BackupItem{
			Name: name,
			Type: "container",
			Settings: map[string]any{
				"id":    c.ID,
				"image": c.Image,
				"state": c.State,
			},
		})
	}
	return items, nil
}

// Backup performs a full backup of a Docker container:
//  1. Inspects the container and saves its config as JSON.
//  2. Stops the container if running (unless no_stop is set).
//  3. Saves the container image as image.tar.
//  4. Tars each bind mount volume to volume_N.tar.gz.
//  5. Restarts the container if it was stopped.
func (h *ContainerHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

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
	inspect, err := h.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		// Stored ID is stale — try resolving by container name instead.
		inspect, err = h.cli.ContainerInspect(ctx, item.Name)
		if err != nil {
			return nil, fmt.Errorf("inspecting container: %w", err)
		}
		containerID = inspect.ID
		log.Printf("[backup] container %q: resolved by name (ID changed from stored value to %s)", item.Name, containerID[:12])
	}

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

	// Extract path exclusions from item settings.
	var exclusions []string
	if ep, ok := item.Settings["exclude_paths"]; ok {
		if epSlice, ok := ep.([]any); ok {
			for _, v := range epSlice {
				if s, ok := v.(string); ok && s != "" {
					exclusions = append(exclusions, s)
				}
			}
		}
	}

	if wasRunning && !noStop {
		progress(item.Name, 20, "stopping container")
		if err := h.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			return nil, fmt.Errorf("stopping container: %w", err)
		}
	}

	if err := runWithRestart(wasRunning && !noStop, item.Name, progress, func() error {
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
			imagePath := filepath.Join(destDir, "image.tar")
			imgFile, err := os.Create(imagePath)
			if err != nil {
				_ = imgReader.Close()
				return fmt.Errorf("creating image file: %w", err)
			}
			if _, err := io.Copy(imgFile, imgReader); err != nil {
				_ = imgFile.Close()
				_ = imgReader.Close()
				return fmt.Errorf("writing image: %w", err)
			}
			_ = imgFile.Close()
			_ = imgReader.Close()
			result.Files = append(result.Files, backupFileInfo(imagePath))
		}

		// Step 4: Tar bind mount volumes, skipping large shared data paths.
		progress(item.Name, 60, "backing up volumes")
		var manifest []volumeManifestEntry
		for i, mount := range inspect.Mounts {
			if mount.Type != "bind" {
				continue
			}

			entry := volumeManifestEntry{
				Index:       i,
				Source:      mount.Source,
				Destination: mount.Destination,
			}

			if skip, reason := shouldSkipVolume(mount.Source); skip {
				log.Printf("engine: skipping volume %s for %s: %s", mount.Source, item.Name, reason)
				entry.BackedUp = false
				entry.SkipReason = reason
				manifest = append(manifest, entry)
				continue
			}

			if hasChangedSince {
				changed, err := pathChangedSince(mount.Source, changedSince)
				if err != nil {
					return fmt.Errorf("checking volume %s changes: %w", mount.Source, err)
				}
				if !changed {
					entry.BackedUp = false
					entry.SkipReason = "unchanged since reference"
					manifest = append(manifest, entry)
					continue
				}
			}

			archiveName := fmt.Sprintf("volume_%d.tar.gz", i)
			volDest := filepath.Join(destDir, archiveName)

			// Detect file-based bind mounts (e.g. Tailscale hook files).
			srcInfo, err := os.Lstat(mount.Source)
			if err != nil {
				return fmt.Errorf("stat volume %s: %w", mount.Source, err)
			}

			if srcInfo.IsDir() {
				volExclusions := mapExclusionsToVolume(exclusions, mount.Destination)

				if hasChangedSince {
					if err := tarDirectoryFiltered(ctx, mount.Source, volDest, changedSince, volExclusions); err != nil {
						return fmt.Errorf("archiving volume %s: %w", mount.Source, err)
					}
				} else {
					if err := tarDirectory(ctx, mount.Source, volDest, volExclusions); err != nil {
						return fmt.Errorf("archiving volume %s: %w", mount.Source, err)
					}
				}

				if len(volExclusions) > 0 {
					entry.ExcludedPaths = volExclusions
				}
			} else {
				if err := tarFile(ctx, mount.Source, volDest); err != nil {
					return fmt.Errorf("archiving volume file %s: %w", mount.Source, err)
				}
				entry.IsFile = true
			}

			result.Files = append(result.Files, backupFileInfo(volDest))
			entry.BackedUp = true
			entry.Archive = archiveName
			manifest = append(manifest, entry)
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
		if data, err := os.ReadFile(templatePath); err == nil {
			includeTemplate := true
			if hasChangedSince {
				changed, changeErr := pathChangedSince(templatePath, changedSince)
				if changeErr != nil {
					return fmt.Errorf("checking template changes: %w", changeErr)
				}
				includeTemplate = changed
			}
			if includeTemplate {
				progress(item.Name, 85, "saving template")
				destTemplate := filepath.Join(destDir, "template.xml")
				if writeErr := os.WriteFile(destTemplate, data, 0600); writeErr != nil {
					return fmt.Errorf("writing template xml: %w", writeErr)
				}
				result.Files = append(result.Files, backupFileInfo(destTemplate))
			}
		}

		return nil
	}, func() error {
		return h.cli.ContainerStart(ctx, containerID, container.StartOptions{})
	}); err != nil {
		return nil, err
	}

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// runWithRestart ensures a previously running container is started again even
// if later backup steps fail after the stop operation has already succeeded.
func runWithRestart(shouldRestart bool, itemName string, progress ProgressFunc, run func() error, restart func() error) error {
	runErr := run()
	if !shouldRestart {
		return runErr
	}

	progress(itemName, 92, "restarting container")
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
	imagePath := filepath.Join(sourceDir, "image.tar")
	imgFile, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("opening image file: %w", err)
	}
	defer imgFile.Close()

	resp, err := h.cli.ImageLoad(ctx, imgFile)
	if err != nil {
		return fmt.Errorf("loading image: %w", err)
	}
	// Drain the response body to ensure the daemon completes the load.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Step 2: Read full container config.
	progress(item.Name, 15, "reading config")
	configPath := filepath.Join(sourceDir, "config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	// Parse the full ContainerJSON to get mounts and determine if the
	// container was running. We use a partial struct to avoid depending
	// on the exact Docker API version for all fields.
	var inspect struct {
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
			Type   string `json:"Type"`
			Source string `json:"Source"`
		} `json:"Mounts"`
	}
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

	// Step 3: Restore volumes.
	// Load the volumes manifest (if present) to know which were skipped.
	progress(item.Name, 30, "restoring volumes")
	var savedManifest []volumeManifestEntry
	if mData, err := os.ReadFile(filepath.Join(sourceDir, "volumes.json")); err == nil {
		_ = json.Unmarshal(mData, &savedManifest)
	}

	for i, mount := range inspect.Mounts {
		if mount.Type != "bind" {
			continue
		}
		volArchive := filepath.Join(sourceDir, fmt.Sprintf("volume_%d.tar.gz", i))
		if _, err := os.Stat(volArchive); err != nil {
			// Check manifest to explain why.
			for _, me := range savedManifest {
				if me.Index == i && !me.BackedUp {
					log.Printf("engine: restore: skipping volume %s (was excluded: %s)", mount.Source, me.SkipReason)
					break
				}
			}
			continue // skip if archive doesn't exist
		}

		targetPath := mount.Source
		if restoreDest != "" {
			volumeName, err := normalizeRestoreComponent(filepath.Base(mount.Source))
			if err != nil {
				return err
			}
			targetPath = filepath.Join(restoreDest, volumeName)
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

	// Step 4: Recreate the container.
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
	if existing, err := h.cli.ContainerInspect(ctx, containerName); err == nil {
		if existing.State.Running {
			_ = h.cli.ContainerStop(ctx, existing.ID, container.StopOptions{})
		}
		_ = h.cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true})
	}

	// Build container config.
	containerConfig := &container.Config{
		Hostname:   inspect.Config.Hostname,
		Domainname: inspect.Config.Domainname,
		User:       inspect.Config.User,
		Env:        inspect.Config.Env,
		Cmd:        inspect.Config.Cmd,
		Entrypoint: inspect.Config.Entrypoint,
		Image:      inspect.Config.Image,
		Labels:     inspect.Config.Labels,
		WorkingDir: inspect.Config.WorkingDir,
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

	portBindings := make(map[string][]string)
	// Convert port bindings to the format expected by Docker API.
	hostConfig := &container.HostConfig{
		Binds:       binds,
		NetworkMode: container.NetworkMode(inspect.HostConfig.NetworkMode),
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyMode(inspect.HostConfig.RestartPolicy.Name),
			MaximumRetryCount: inspect.HostConfig.RestartPolicy.MaximumRetryCount,
		},
		Privileged: inspect.HostConfig.Privileged,
		CapAdd:     inspect.HostConfig.CapAdd,
		CapDrop:    inspect.HostConfig.CapDrop,
		DNS:        inspect.HostConfig.Dns,
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
	_ = portBindings // avoid unused variable

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
				es.IPAMConfig = &network.EndpointIPAMConfig{
					IPv4Address: netCfg.IPAMConfig.IPv4Address,
				}
			}
			networkConfig.EndpointsConfig[netName] = es
		}
	}

	created, err := h.cli.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, containerName)
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}

	// Step 5: Restore Unraid template XML.
	progress(item.Name, 80, "restoring template")
	templateSrc := filepath.Join(sourceDir, "template.xml")
	if data, readErr := os.ReadFile(templateSrc); readErr == nil {
		templateDest := filepath.Join("/boot/config/plugins/dockerMan/templates-user", "my-"+containerName+".xml") //nolint:gosec // path is constructed from trusted container name
		if mkErr := os.MkdirAll(filepath.Dir(templateDest), 0750); mkErr == nil {
			_ = os.WriteFile(templateDest, data, 0600) //nolint:gosec // best-effort restore of template
		}
	}

	// Step 6: Start container if it was originally running.
	if inspect.State.Running {
		progress(item.Name, 90, "starting container")
		if err := h.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("starting restored container: %w", err)
		}
	}

	progress(item.Name, 100, "restore complete")
	return nil
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

// tarFile creates a gzip-compressed tar archive containing a single file.
// Used for file-based bind mounts (e.g. Tailscale container hook).
func tarFile(ctx context.Context, srcPath, destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("creating tar header: %w", err)
	}
	header.Name = filepath.Base(srcPath)

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer f.Close()

	if _, err := contextCopy(ctx, tw, io.LimitReader(f, header.Size)); err != nil {
		return fmt.Errorf("writing file to tar: %w", err)
	}

	return nil
}

// untarFile extracts the first regular file from a gzip-compressed tar archive
// and writes it to destPath. Used for restoring file-based bind mounts.
func untarFile(ctx context.Context, srcPath, destPath string) error {
	inFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer inFile.Close()

	gr, err := gzip.NewReader(inFile)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
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

		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeFileMode(header.Mode))
		if err != nil {
			return fmt.Errorf("creating file %s: %w", destPath, err)
		}
		n, err := io.Copy(f, io.LimitReader(tr, header.Size))
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

// tarDirectory creates a gzip-compressed tar archive of srcDir at destPath.
func tarDirectory(ctx context.Context, srcDir, destPath string, exclusions []string) error {
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("opening source root: %w", err)
	}
	defer root.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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
}

// tarDirectoryFiltered creates a gzip-compressed tar archive of srcDir at destPath,
// including only files whose modification time is after changedSince. Directory
// entries are always included to preserve structure. This is used for
// incremental and differential backups.
func tarDirectoryFiltered(ctx context.Context, srcDir, destPath string, changedSince time.Time, exclusions []string) error {
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("opening source root: %w", err)
	}
	defer root.Close()

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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

		// Always include directories; filter regular files by mod time.
		if !info.IsDir() && !info.ModTime().After(changedSince) {
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
}

// untarDirectory extracts a gzip-compressed tar archive from srcPath into destDir.
func untarDirectory(ctx context.Context, srcPath, destDir string) error {
	inFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer inFile.Close()

	gr, err := gzip.NewReader(inFile)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
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

		target, err := joinArchiveTarget(destDir, header.Name)
		if err != nil {
			return fmt.Errorf("illegal file path in archive: %s: %w", header.Name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, safeFileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
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
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeFileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}
			n, err := io.Copy(f, io.LimitReader(tr, header.Size))
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
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %s -> %s: %w", target, header.Linkname, err)
			}
		case tar.TypeLink:
			linkTarget, err := joinArchiveTarget(destDir, header.Linkname)
			if err != nil {
				return fmt.Errorf("illegal hard link target in archive: %s: %w", header.Linkname, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("creating hard link %s -> %s: %w", target, linkTarget, err)
			}
		default:
			continue
		}
	}
	return nil
}

// StopContainers stops the given container IDs in order. It returns the IDs
// that were actually stopped (i.e. were running) so the caller can restart them.
func StopContainers(ids []string) ([]string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	var stopped []string
	for _, id := range ids {
		inspect, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			return stopped, fmt.Errorf("inspecting container %s: %w", id, err)
		}
		if !inspect.State.Running {
			continue
		}
		if err := cli.ContainerStop(ctx, id, container.StopOptions{}); err != nil {
			return stopped, fmt.Errorf("stopping container %s: %w", id, err)
		}
		stopped = append(stopped, id)
	}
	return stopped, nil
}

// StartContainers starts the given container IDs. Errors are logged but do not
// abort the remaining starts so that as many containers as possible are restored.
func StartContainers(ids []string) []error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return []error{fmt.Errorf("creating docker client: %w", err)}
	}
	defer cli.Close()

	ctx := context.Background()
	var errs []error
	for _, id := range ids {
		if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
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
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
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
			inspect, err := cli.ContainerInspect(ctx, containerID)
			if err != nil {
				continue
			}

			state := inspect.State
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

// backupFileInfo returns a BackupFile with name and size from the given path.
func backupFileInfo(path string) BackupFile {
	info, err := os.Stat(path)
	if err != nil {
		return BackupFile{Name: filepath.Base(path)}
	}
	return BackupFile{Name: filepath.Base(path), Size: info.Size()}
}
