// Command gendocs renders the docsmeta catalog into the settings-reference
// pages under docs/reference. The output is gitignored and rebuilt in CI; run
// it via `go generate ./...` or `go run ./cmd/gendocs`.
package main

//go:generate go run .

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// outSubdir is the reference directory relative to the repository root.
var outSubdir = filepath.Join("docs", "reference")

// repoRoot derives the repository root from this source file's location so the
// generator always writes to <root>/docs/reference regardless of the working
// directory. `go generate ./...` runs directives from the package directory
// (cmd/gendocs), not the repo root, so a plain relative path would misfire.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine caller path")
	}
	// file == <root>/cmd/gendocs/main.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func main() {
	root, err := repoRoot()
	if err != nil {
		log.Fatalf("gendocs: %v", err)
	}
	outDir := filepath.Join(root, outSubdir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("gendocs: create %s: %v", outDir, err)
	}

	pages := map[string]string{
		"app-settings.md":   docsmeta.RenderAppSettings(),
		"notifications.md":  docsmeta.RenderNotifications(),
		"job-config.md":     docsmeta.RenderStruct("Job Configuration", db.Job{}),
		"storage-sftp.md":   docsmeta.RenderStruct("Storage: SFTP", storage.SFTPConfig{}),
		"storage-smb.md":    docsmeta.RenderStruct("Storage: SMB", storage.SMBConfig{}),
		"storage-nfs.md":    docsmeta.RenderStruct("Storage: NFS", storage.NFSConfig{}),
		"storage-webdav.md": docsmeta.RenderStruct("Storage: WebDAV", storage.WebDAVConfig{}),
		"storage-s3.md":     docsmeta.RenderStruct("Storage: S3", storage.S3Config{}),
		"storage-local.md":  docsmeta.RenderLocal(),
	}

	for name, content := range pages {
		if err := writePage(outDir, name, content); err != nil {
			log.Fatalf("gendocs: %v", err)
		}
	}
	fmt.Printf("gendocs: wrote %d pages to %s\n", len(pages), outDir)
}

func writePage(outDir, name, content string) error {
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
