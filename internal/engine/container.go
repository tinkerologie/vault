package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

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
func (h *ContainerHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	ctx := context.Background()
	result := &BackupResult{ItemName: item.Name}

	containerID, _ := item.Settings["id"].(string)
	if containerID == "" {
		return nil, fmt.Errorf("container id not found in settings")
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	// Step 1: Inspect and save config.
	progress(item.Name, 10, "inspecting container")
	inspect, err := h.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspecting container: %w", err)
	}

	configPath := filepath.Join(destDir, "config.json")
	configData, err := json.MarshalIndent(inspect, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling config: %w", err)
	}
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return nil, fmt.Errorf("writing config: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(configPath))

	// Step 2: Stop container if running (unless no_stop setting).
	wasRunning := inspect.State.Running
	noStop, _ := item.Settings["no_stop"].(bool)
	if wasRunning && !noStop {
		progress(item.Name, 20, "stopping container")
		if err := h.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			return nil, fmt.Errorf("stopping container: %w", err)
		}
	}

	// Step 3: Save image.
	progress(item.Name, 40, "saving image")
	imageName := inspect.Config.Image
	imgReader, err := h.cli.ImageSave(ctx, []string{imageName})
	if err != nil {
		return nil, fmt.Errorf("saving image: %w", err)
	}
	imagePath := filepath.Join(destDir, "image.tar")
	imgFile, err := os.Create(imagePath)
	if err != nil {
		_ = imgReader.Close()
		return nil, fmt.Errorf("creating image file: %w", err)
	}
	if _, err := io.Copy(imgFile, imgReader); err != nil {
		_ = imgFile.Close()
		_ = imgReader.Close()
		return nil, fmt.Errorf("writing image: %w", err)
	}
	_ = imgFile.Close()
	_ = imgReader.Close()
	result.Files = append(result.Files, backupFileInfo(imagePath))

	// Step 4: Tar bind mount volumes.
	progress(item.Name, 60, "backing up volumes")
	for i, mount := range inspect.Mounts {
		if mount.Type != "bind" {
			continue
		}
		volDest := filepath.Join(destDir, fmt.Sprintf("volume_%d.tar.gz", i))
		if err := tarDirectory(mount.Source, volDest); err != nil {
			return nil, fmt.Errorf("archiving volume %s: %w", mount.Source, err)
		}
		result.Files = append(result.Files, backupFileInfo(volDest))
	}

	// Step 5: Restart container if it was running.
	if wasRunning && !noStop {
		progress(item.Name, 90, "restarting container")
		if err := h.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			return nil, fmt.Errorf("restarting container: %w", err)
		}
	}

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// Restore restores a Docker container from a backup directory:
//  1. Loads the image from image.tar.
//  2. Reads the saved config.
//  3. Restores volume data from tar.gz archives.
func (h *ContainerHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
	ctx := context.Background()

	// Step 1: Load image.
	progress(item.Name, 10, "loading image")
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
	resp.Body.Close()

	// Step 2: Read config.
	progress(item.Name, 30, "reading config")
	configPath := filepath.Join(sourceDir, "config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	var inspect struct {
		Mounts []struct {
			Type   string `json:"Type"`
			Source string `json:"Source"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal(configData, &inspect); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Step 3: Restore volumes.
	progress(item.Name, 50, "restoring volumes")
	for i, mount := range inspect.Mounts {
		if mount.Type != "bind" {
			continue
		}
		volArchive := filepath.Join(sourceDir, fmt.Sprintf("volume_%d.tar.gz", i))
		if _, err := os.Stat(volArchive); err != nil {
			continue // skip if archive doesn't exist
		}
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			return fmt.Errorf("creating volume dir %s: %w", mount.Source, err)
		}
		if err := untarDirectory(volArchive, mount.Source); err != nil {
			return fmt.Errorf("restoring volume %s: %w", mount.Source, err)
		}
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

// tarDirectory creates a gzip-compressed tar archive of srcDir at destPath.
func tarDirectory(srcDir, destPath string) error {
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
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", rel, err)
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", rel, err)
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening file %s: %w", rel, err)
		}
		defer f.Close()

		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("writing file %s to tar: %w", rel, err)
		}
		return nil
	})
}

// untarDirectory extracts a gzip-compressed tar archive from srcPath into destDir.
func untarDirectory(srcPath, destDir string) error {
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
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Guard against zip-slip.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", target, err)
			}
			f.Close()
		}
	}
	return nil
}

// backupFileInfo returns a BackupFile with name and size from the given path.
func backupFileInfo(path string) BackupFile {
	info, err := os.Stat(path)
	if err != nil {
		return BackupFile{Name: filepath.Base(path)}
	}
	return BackupFile{Name: filepath.Base(path), Size: info.Size()}
}
