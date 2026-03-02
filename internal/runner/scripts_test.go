package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunScript(t *testing.T) {
	t.Parallel()

	t.Run("empty script", func(t *testing.T) {
		t.Parallel()
		out, err := runScript("", time.Second)
		if err != nil {
			t.Errorf("runScript('') error = %v", err)
		}
		if out != "" {
			t.Errorf("runScript('') output = %q, want empty", out)
		}
	})

	t.Run("relative path rejected", func(t *testing.T) {
		t.Parallel()
		_, err := runScript("scripts/run.sh", time.Second)
		if err == nil {
			t.Error("expected error for relative path")
		}
	})

	t.Run("nonexistent script", func(t *testing.T) {
		t.Parallel()
		_, err := runScript("/usr/local/bin/vault-nonexistent-test-script", time.Second)
		if err == nil {
			t.Error("expected error for nonexistent script")
		}
	})

	t.Run("directory rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := runScript(dir, time.Second)
		if err == nil {
			t.Error("expected error for directory")
		}
	})

	t.Run("non-executable rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		script := filepath.Join(dir, "test.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := runScript(script, time.Second)
		if err == nil {
			t.Error("expected error for non-executable script")
		}
	})

	t.Run("successful script", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		script := filepath.Join(dir, "test.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello"), 0755); err != nil {
			t.Fatal(err)
		}
		out, err := runScript(script, 5*time.Second)
		if err != nil {
			t.Errorf("runScript() error = %v", err)
		}
		if out != "hello\n" {
			t.Errorf("runScript() output = %q, want %q", out, "hello\n")
		}
	})

	t.Run("failing script", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		script := filepath.Join(dir, "fail.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1"), 0755); err != nil {
			t.Fatal(err)
		}
		_, err := runScript(script, 5*time.Second)
		if err == nil {
			t.Error("expected error for failing script")
		}
	})
}
