package runner

import (
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestAnnotateRestorePointsMarksBrokenChain(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	job := db.Job{Name: "broken", RetentionCount: 1}
	points := []db.RestorePoint{{
		ID:                   2,
		BackupType:           "incremental",
		ParentRestorePointID: 99,
		CreatedAt:            now,
	}}

	annotated := annotateRestorePoints(job, points, now)
	if len(annotated) != 1 {
		t.Fatalf("got %d points, want 1", len(annotated))
	}
	if annotated[0].ChainStatus != "broken" {
		t.Fatalf("ChainStatus = %q, want broken", annotated[0].ChainStatus)
	}
	if annotated[0].MissingParentRestorePointID != 99 {
		t.Fatalf("MissingParentRestorePointID = %d, want 99", annotated[0].MissingParentRestorePointID)
	}
	if annotated[0].ChainWarning == "" {
		t.Fatal("expected non-empty chain warning")
	}
}

func TestAnnotateRestorePointsMarksRetentionPreservedParent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	job := db.Job{Name: "retention", RetentionCount: 1}
	points := []db.RestorePoint{
		{
			ID:         10,
			BackupType: "full",
			CreatedAt:  now.Add(-2 * time.Hour),
		},
		{
			ID:                   11,
			BackupType:           "differential",
			ParentRestorePointID: 10,
			CreatedAt:            now,
		},
	}

	annotated := annotateRestorePoints(job, points, now)
	byID := make(map[int64]AnnotatedRestorePoint, len(annotated))
	for _, rp := range annotated {
		byID[rp.ID] = rp
	}

	child := byID[11]
	if child.ChainStatus != "healthy" {
		t.Fatalf("child ChainStatus = %q, want healthy", child.ChainStatus)
	}
	if child.ChainDepth != 2 {
		t.Fatalf("child ChainDepth = %d, want 2", child.ChainDepth)
	}

	parent := byID[10]
	if parent.ChainStatus != "standalone" {
		t.Fatalf("parent ChainStatus = %q, want standalone", parent.ChainStatus)
	}
	if !parent.RetentionPreserved {
		t.Fatal("expected parent restore point to be marked retention-preserved")
	}
	if parent.RetentionPreservedFor != 1 {
		t.Fatalf("RetentionPreservedFor = %d, want 1", parent.RetentionPreservedFor)
	}
}

func TestAnnotateRestorePointsUsesNewestOrderForRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	job := db.Job{Name: "unsorted", RetentionCount: 1}
	points := []db.RestorePoint{
		{
			ID:         20,
			BackupType: "full",
			CreatedAt:  now.Add(-2 * time.Hour),
		},
		{
			ID:                   22,
			BackupType:           "incremental",
			ParentRestorePointID: 21,
			CreatedAt:            now,
		},
		{
			ID:                   21,
			BackupType:           "incremental",
			ParentRestorePointID: 20,
			CreatedAt:            now.Add(-time.Hour),
		},
	}

	annotated := annotateRestorePoints(job, points, now)
	byID := make(map[int64]AnnotatedRestorePoint, len(annotated))
	for _, rp := range annotated {
		byID[rp.ID] = rp
	}

	if !byID[21].RetentionPreserved {
		t.Fatal("expected intermediate restore point to be preserved for the newest child")
	}
	if !byID[20].RetentionPreserved {
		t.Fatal("expected base restore point to be preserved for the newest child")
	}
	if byID[22].ChainDepth != 3 {
		t.Fatalf("latest ChainDepth = %d, want 3", byID[22].ChainDepth)
	}
}

func TestAnnotateRestorePointsSurfacesBaseFullBackup(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC)
	inc1Time := time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC)
	diffTime := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)

	job := db.Job{Name: "base-full-chain", RetentionCount: 5}
	points := []db.RestorePoint{
		{
			ID:         100,
			BackupType: "full",
			SizeBytes:  1024 * 1024,
			CreatedAt:  baseTime,
		},
		{
			ID:                   101,
			BackupType:           "incremental",
			ParentRestorePointID: 100,
			SizeBytes:            512 * 1024,
			CreatedAt:            inc1Time,
		},
		{
			ID:                   102,
			BackupType:           "differential",
			ParentRestorePointID: 100,
			SizeBytes:            768 * 1024,
			CreatedAt:            diffTime,
		},
		{
			ID:                   103,
			BackupType:           "incremental",
			ParentRestorePointID: 999,
			SizeBytes:            256 * 1024,
			CreatedAt:            diffTime.Add(time.Hour),
		},
	}

	annotated := annotateRestorePoints(job, points, diffTime.Add(2*time.Hour))
	byID := make(map[int64]AnnotatedRestorePoint, len(annotated))
	for _, rp := range annotated {
		byID[rp.ID] = rp
	}

	// Standalone full point reports itself as base full backup
	fullPt := byID[100]
	if fullPt.BaseFullRestorePointID != 100 {
		t.Errorf("full BaseFullRestorePointID = %d, want 100", fullPt.BaseFullRestorePointID)
	}
	if fullPt.BaseFullCreatedAt == nil || !fullPt.BaseFullCreatedAt.Equal(baseTime) {
		t.Errorf("full BaseFullCreatedAt = %v, want %v", fullPt.BaseFullCreatedAt, baseTime)
	}
	if fullPt.BaseFullSizeBytes != 1024*1024 {
		t.Errorf("full BaseFullSizeBytes = %d, want %d", fullPt.BaseFullSizeBytes, 1024*1024)
	}

	// Incremental child point reports base full backup
	incPt := byID[101]
	if incPt.BaseFullRestorePointID != 100 {
		t.Errorf("inc BaseFullRestorePointID = %d, want 100", incPt.BaseFullRestorePointID)
	}
	if incPt.BaseFullCreatedAt == nil || !incPt.BaseFullCreatedAt.Equal(baseTime) {
		t.Errorf("inc BaseFullCreatedAt = %v, want %v", incPt.BaseFullCreatedAt, baseTime)
	}
	if incPt.BaseFullSizeBytes != 1024*1024 {
		t.Errorf("inc BaseFullSizeBytes = %d, want %d", incPt.BaseFullSizeBytes, 1024*1024)
	}

	// Differential child point reports base full backup
	diffPt := byID[102]
	if diffPt.BaseFullRestorePointID != 100 {
		t.Errorf("diff BaseFullRestorePointID = %d, want 100", diffPt.BaseFullRestorePointID)
	}
	if diffPt.BaseFullCreatedAt == nil || !diffPt.BaseFullCreatedAt.Equal(baseTime) {
		t.Errorf("diff BaseFullCreatedAt = %v, want %v", diffPt.BaseFullCreatedAt, baseTime)
	}
	if diffPt.BaseFullSizeBytes != 1024*1024 {
		t.Errorf("diff BaseFullSizeBytes = %d, want %d", diffPt.BaseFullSizeBytes, 1024*1024)
	}

	// Broken chain does not report base full backup
	brokenPt := byID[103]
	if brokenPt.BaseFullRestorePointID != 0 {
		t.Errorf("broken BaseFullRestorePointID = %d, want 0", brokenPt.BaseFullRestorePointID)
	}
	if brokenPt.BaseFullCreatedAt != nil {
		t.Errorf("broken BaseFullCreatedAt = %v, want nil", brokenPt.BaseFullCreatedAt)
	}
	if brokenPt.BaseFullSizeBytes != 0 {
		t.Errorf("broken BaseFullSizeBytes = %d, want 0", brokenPt.BaseFullSizeBytes)
	}
}
