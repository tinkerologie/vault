package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

func TestResolveVanishedBackupJobNoCompletedRecordWithArtifacts(t *testing.T) {
	t.Parallel()

	// Regression for issue #160: libvirt reports "no completed-job record"
	// as a successful stats query with job type DomainJobNone. That carries
	// no verdict — with all artifacts on disk the backup must be treated as
	// successful, not "backup job ended unexpectedly: DomainJobNone".
	done, err := resolveVanishedBackupJob(libvirt.DomainJobNone, nil, nil, true)
	if !done {
		t.Fatal("expected a verdict when artifacts exist")
	}
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestResolveVanishedBackupJobNoCompletedRecordNoArtifacts(t *testing.T) {
	t.Parallel()

	done, err := resolveVanishedBackupJob(libvirt.DomainJobNone, nil, nil, false)
	if done {
		t.Fatalf("expected no verdict without completed record or artifacts, got err=%v", err)
	}
	if err != nil {
		t.Fatalf("expected nil error when no verdict, got: %v", err)
	}
}

func TestResolveVanishedBackupJobCompletedSuccess(t *testing.T) {
	t.Parallel()

	params := []libvirt.TypedParam{
		{Field: "success", Value: libvirt.TypedParamValue{I: int32(1)}},
	}
	done, err := resolveVanishedBackupJob(libvirt.DomainJobCompleted, params, nil, false)
	if !done {
		t.Fatal("expected a verdict from a completed-job record")
	}
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestResolveVanishedBackupJobCompletedFailure(t *testing.T) {
	t.Parallel()

	params := []libvirt.TypedParam{
		{Field: "success", Value: libvirt.TypedParamValue{I: int32(0)}},
		{Field: "errmsg", Value: libvirt.TypedParamValue{I: "No space left on device"}},
	}
	done, err := resolveVanishedBackupJob(libvirt.DomainJobCompleted, params, nil, true)
	if !done {
		t.Fatal("expected a verdict from a completed-job record")
	}
	if err == nil || !strings.Contains(err.Error(), "No space left on device") {
		t.Fatalf("expected failure with libvirt errmsg, got: %v", err)
	}
}

func TestResolveVanishedBackupJobFailedRecord(t *testing.T) {
	t.Parallel()

	done, err := resolveVanishedBackupJob(libvirt.DomainJobFailed, nil, nil, true)
	if !done {
		t.Fatal("expected a verdict from a failed-job record")
	}
	if err == nil {
		t.Fatal("expected error for a failed job record even when artifacts exist")
	}
}

func TestResolveVanishedBackupJobStatsError(t *testing.T) {
	t.Parallel()

	statsErr := errors.New("connection reset")

	done, err := resolveVanishedBackupJob(libvirt.DomainJobNone, nil, statsErr, true)
	if !done || err != nil {
		t.Fatalf("expected success from artifacts when completed stats unavailable, got done=%v err=%v", done, err)
	}

	done, err = resolveVanishedBackupJob(libvirt.DomainJobNone, nil, statsErr, false)
	if done {
		t.Fatalf("expected no verdict when stats unavailable and artifacts missing, got err=%v", err)
	}
	if err != nil {
		t.Fatalf("expected nil error when no verdict, got: %v", err)
	}
}

func TestBackupJobErrorReportsErrmsg(t *testing.T) {
	t.Parallel()

	params := []libvirt.TypedParam{
		{Field: "errmsg", Value: libvirt.TypedParamValue{I: "backup target write failed"}},
	}
	err := backupJobError(libvirt.DomainJobFailed, params)
	if err == nil || !strings.Contains(err.Error(), "backup target write failed") {
		t.Fatalf("expected errmsg in error, got: %v", err)
	}
}

func TestDescribeMissingBackupArtifacts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	present := filepath.Join(dir, "vdisk0.img")
	if err := os.WriteFile(present, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "vdisk1.qcow2")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	artifacts := []vmBackupArtifact{
		{BackupFile: "vdisk0.img", TargetPath: present},
		{BackupFile: "vdisk1.qcow2", TargetPath: empty},
		{BackupFile: "vdisk2.img", TargetPath: filepath.Join(dir, "vdisk2.img")},
	}

	desc := describeMissingBackupArtifacts(artifacts)
	if strings.Contains(desc, "vdisk0.img") {
		t.Fatalf("present artifact should not be reported missing: %s", desc)
	}
	if !strings.Contains(desc, "vdisk1.qcow2") || !strings.Contains(desc, "vdisk2.img") {
		t.Fatalf("expected empty and absent artifacts to be reported: %s", desc)
	}
}

func TestTypedParamHelpers(t *testing.T) {
	t.Parallel()

	params := []libvirt.TypedParam{
		{Field: "Success", Value: libvirt.TypedParamValue{I: int32(1)}},
		{Field: "errmsg", Value: libvirt.TypedParamValue{I: "boom"}},
		{Field: "data_processed", Value: libvirt.TypedParamValue{I: uint64(42)}},
		{Field: "disk_total", Value: libvirt.TypedParamValue{I: int64(-1)}},
	}

	// Field names are normalized (case folded, separators stripped).
	if got, ok := typedParamBool(params, "success"); !ok || !got {
		t.Fatalf("typedParamBool(success) = %v, %v", got, ok)
	}
	if _, ok := typedParamBool(params, "absent"); ok {
		t.Fatal("typedParamBool should not match absent key")
	}
	if _, ok := typedParamBool(params); ok {
		t.Fatal("typedParamBool with no keys should not match")
	}

	if got := typedParamString(params, "errmsg", "error"); got != "boom" {
		t.Fatalf("typedParamString = %q", got)
	}
	if got := typedParamString(params, "missing"); got != "" {
		t.Fatalf("typedParamString(missing) = %q", got)
	}

	if got, ok := typedParamUint64(params, "dataprocessed"); !ok || got != 42 {
		t.Fatalf("typedParamUint64 = %d, %v", got, ok)
	}
	// Negative signed values must not wrap around.
	if _, ok := typedParamUint64(params, "disktotal"); ok {
		t.Fatal("typedParamUint64 should reject negative values")
	}

	if !typedParamValueBool(libvirt.TypedParamValue{I: true}) {
		t.Fatal("typedParamValueBool(true) = false")
	}
	if typedParamValueBool(libvirt.TypedParamValue{I: "yes"}) {
		t.Fatal("typedParamValueBool should be false for unsupported types")
	}

	if got := normalizeTypedParamField("File_Processed"); got != "fileprocessed" {
		t.Fatalf("normalizeTypedParamField = %q", got)
	}
}

func TestBackupArtifactsExist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "vdisk0.img")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !backupArtifactsExist([]vmBackupArtifact{{TargetPath: path}}) {
		t.Fatal("expected artifacts to exist")
	}
	if backupArtifactsExist([]vmBackupArtifact{{TargetPath: filepath.Join(dir, "missing")}}) {
		t.Fatal("expected missing artifact to fail the check")
	}
	if backupArtifactsExist(nil) {
		t.Fatal("expected empty artifact list to fail the check")
	}
}
