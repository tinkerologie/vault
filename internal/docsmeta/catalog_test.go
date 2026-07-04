package docsmeta_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// findRepoRoot walks up from the test's package directory until it finds go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// getSettingCall matches GetSetting / GetSettingInt / GetSettingBool call sites
// with a string-literal key, capturing the key.
var getSettingCall = regexp.MustCompile(`GetSetting(?:Int|Bool)?\("([^"]+)"`)

// Criterion 1: every string-literal key passed to a GetSetting* accessor in
// non-test code under internal/ must be registered in docsmeta.AppSettings.
// This guards the runtime panic in DefaultFor for an unregistered key.
func TestAllGetSettingKeysAreRegistered(t *testing.T) {
	registered := make(map[string]bool, len(docsmeta.AppSettings))
	for _, s := range docsmeta.AppSettings {
		registered[s.Key] = true
	}

	root := findRepoRoot(t)
	internalDir := filepath.Join(root, "internal")

	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files and the accessor definitions themselves.
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "settings_repo.go" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range getSettingCall.FindAllStringSubmatch(string(content), -1) {
			key := m[1]
			if !registered[key] {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("GetSetting key %q used in %s is not registered in docsmeta.AppSettings", key, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", internalDir, err)
	}
}

// registeredConfigStructs is the set of config structs whose exported fields the
// generated reference documents. The local storage config is an anonymous inline
// struct (no named type) and is documented under the synthetic key
// LocalConfig.Path, so it is intentionally excluded here.
var registeredConfigStructs = []interface{}{
	db.Job{},
	db.StorageDestination{},
	storage.SFTPConfig{},
	storage.SMBConfig{},
	storage.NFSConfig{},
	storage.WebDAVConfig{},
	storage.S3Config{},
}

// Criterion 2: every exported field of a registered config struct must be
// documented in FieldDocs or listed in InternalFields.
func TestAllExportedConfigFieldsAreDocumented(t *testing.T) {
	for _, v := range registeredConfigStructs {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue // unexported
			}
			key := rt.Name() + "." + f.Name
			_, documented := docsmeta.FieldDocs[key]
			if !documented && !docsmeta.InternalFields[key] {
				t.Errorf("exported field %s is in neither docsmeta.FieldDocs nor docsmeta.InternalFields", key)
			}
		}
	}
}

// Criterion 3: typed defaults must be parseable for their declared Type.
func TestTypedDefaultsAreWellFormed(t *testing.T) {
	validBool := map[string]bool{"true": true, "false": true, "1": true, "0": true}
	for _, s := range docsmeta.AppSettings {
		switch s.Type {
		case "int":
			if _, err := strconv.Atoi(s.Default); err != nil {
				t.Errorf("setting %q has Type=int but Default %q is not a valid int: %v", s.Key, s.Default, err)
			}
		case "bool":
			if !validBool[s.Default] {
				t.Errorf("setting %q has Type=bool but Default %q is not one of true/false/1/0", s.Key, s.Default)
			}
		}
	}
}

// Criterion 4: internal secret/auth sentinels must default to "" so that
// "not configured" detection stays correct.
func TestInternalDefaultsAreEmpty(t *testing.T) {
	for _, s := range docsmeta.AppSettings {
		if s.Group == docsmeta.GroupInternal && s.Default != "" {
			t.Errorf("internal setting %q must have an empty Default (got %q) so unconfigured detection stays correct", s.Key, s.Default)
		}
	}
}

// TestAppSettingsKeysAreUnique guards against a duplicate Key silently shadowing
// another in the DefaultFor lookup index.
func TestAppSettingsKeysAreUnique(t *testing.T) {
	seen := make(map[string]bool, len(docsmeta.AppSettings))
	for _, s := range docsmeta.AppSettings {
		if seen[s.Key] {
			t.Errorf("duplicate AppSettings key %q", s.Key)
		}
		seen[s.Key] = true
	}
}
