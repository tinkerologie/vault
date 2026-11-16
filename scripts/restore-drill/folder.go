package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tinkerologie/vault/internal/safepath"
)

type folderManifest struct {
	Directories []string          `json:"directories"`
	Files       map[string]string `json:"files"`
}

func createFixture(dir, manifestPath string) error {
	dir, manifestPath, err := fixturePaths(dir, manifestPath)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create drill manifest: %w", err)
	}
	if err := writeFixture(dir, file); err != nil {
		return errors.Join(err, file.Close(), os.Remove(manifestPath))
	}
	return file.Close()
}

func writeFixture(dir string, manifestFile io.Writer) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create drill directory: %w", err)
	}
	for _, name := range []string{"nested", "nested/empty-dir"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			return fmt.Errorf("create fixture directory: %w", err)
		}
	}
	files := map[string][]byte{
		"note.txt":        []byte("Vault folder restore drill\n"),
		"nested/data.bin": {0x00, 0x01, 0x7f, 0x80, 0xff},
		"nested/empty":    {},
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return fmt.Errorf("write fixture file: %w", err)
		}
	}
	manifest, err := scanFixture(dir)
	if err != nil {
		return err
	}
	return json.NewEncoder(manifestFile).Encode(manifest)
}

func checkFixture(dir, manifestPath string, output io.Writer) error {
	dir, manifestPath, err := fixturePaths(dir, manifestPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read drill manifest: %w", err)
	}
	var want folderManifest
	if err := json.Unmarshal(data, &want); err != nil {
		return fmt.Errorf("parse drill manifest: %w", err)
	}
	if len(want.Directories) == 0 || len(want.Files) == 0 {
		return errors.New("drill manifest must include both directories and files")
	}
	got, err := scanFixture(dir)
	if err != nil {
		return err
	}
	var diffs []string
	for _, name := range want.Directories {
		if !slices.Contains(got.Directories, name) {
			diffs = append(diffs, "missing "+name)
		}
	}
	for _, name := range got.Directories {
		if !slices.Contains(want.Directories, name) {
			diffs = append(diffs, "unexpected "+name)
		}
	}
	for name, digest := range want.Files {
		switch actual, ok := got.Files[name]; {
		case !ok:
			diffs = append(diffs, "missing "+name)
		case actual != digest:
			diffs = append(diffs, "changed "+name)
		}
	}
	for name := range got.Files {
		if _, ok := want.Files[name]; !ok {
			diffs = append(diffs, "unexpected "+name)
		}
	}
	slices.Sort(diffs)
	if len(diffs) > 0 {
		return errors.New(strings.Join(diffs, "\n"))
	}
	_, err = fmt.Fprintf(output, "Folder matches manifest (%d files, %d directories)\n", len(got.Files), len(got.Directories))
	return err
}

func fixturePaths(dir, manifest string) (string, string, error) {
	dir, err := fixtureLeaf(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolve drill directory: %w", err)
	}
	manifest, err = fixtureLeaf(manifest)
	if err != nil {
		return "", "", fmt.Errorf("resolve drill manifest: %w", err)
	}
	relative, err := filepath.Rel(dir, manifest)
	if err != nil {
		return "", "", fmt.Errorf("compare drill directory and manifest paths: %w", err)
	}
	if filepath.IsLocal(relative) {
		return "", "", errors.New("drill manifest must be outside the fixture directory")
	}
	return dir, manifest, nil
}

func fixtureLeaf(name string) (string, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}

func scanFixture(dir string) (folderManifest, error) {
	manifest := folderManifest{Files: make(map[string]string)}
	root, err := safepath.OpenPinnedDir(dir)
	if err != nil {
		return folderManifest{}, fmt.Errorf("open drill directory: %w", err)
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		if entry.IsDir() {
			manifest.Directories = append(manifest.Directories, name)
			return nil
		}
		digest, err := digestFixtureFile(root, name, entry)
		if err != nil {
			return fmt.Errorf("check fixture entry %q: %w", name, err)
		}
		manifest.Files[name] = digest
		return nil
	})
	return manifest, err
}

func digestFixtureFile(root *os.Root, name string, entry fs.DirEntry) (string, error) {
	before, err := entry.Info()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return "", errors.New("fixture entry changed during the check")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
