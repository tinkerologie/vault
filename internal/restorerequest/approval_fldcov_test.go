package restorerequest

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

type fldcovService struct {
	*Service
	db  *db.DB
	job int64
	mu  *sync.Mutex
}

func newFldcovService(t *testing.T) fldcovService {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dest, err := database.CreateStorageDestination(db.StorageDestination{Name: "Local", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	job, err := database.CreateJob(db.Job{Name: "Fixture Job", StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	mu := &sync.Mutex{}
	service := New(database, nil, mu, runner.New(database, nil, nil), nil, nil)
	return fldcovService{Service: service, db: database, job: job, mu: mu}
}

// admit stores a pending request whose operation is the given JSON verbatim.
func (f fldcovService) admit(t *testing.T, id, operation string) string {
	t.Helper()
	now := time.Now()
	request := db.RestoreRequest{
		ID: id, OwnerDigest: "owner-" + id, RequesterName: "fixture", Operation: json.RawMessage(operation),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if _, created, err := f.db.AdmitRestoreRequest(t.Context(), request, id, id, 5, 5, now.Add(-time.Hour)); err != nil || !created {
		t.Fatalf("admit: %v %v", created, err)
	}
	return reviewDigest(json.RawMessage(operation))
}

func fldcovID(c string) string { return strings.Repeat(c, 32) }

func TestApproveRefusesBeforeClaimingRun(t *testing.T) {
	f := newFldcovService(t)
	ctx := t.Context()

	if _, _, err := f.Approve(ctx, "NOT-AN-ID", "", "root", ""); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}
	if _, _, err := f.Approve(ctx, fldcovID("0"), "", "root", ""); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}

	cases := []struct {
		name, id, operation string
		passphrase          string
		check               func(error) bool
	}{
		{"undecodable operation", fldcovID("1"), `{"eligible_for_approval":"yes"}`, "",
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "decoding reviewed") }},
		{"metadata review pending", fldcovID("2"), `{"eligible_for_approval":false,"requires_metadata_review":true}`, "",
			func(err error) bool {
				var invalid Invalid
				return errors.As(err, &invalid) && strings.Contains(err.Error(), "unlock")
			}},
		{"manual container", fldcovID("3"), `{"eligible_for_approval":false,"manual_restore_required":true}`, "",
			func(err error) bool { return errors.Is(err, manualContainerRestore) }},
		{"ineligible", fldcovID("4"), `{"eligible_for_approval":false}`, "",
			func(err error) bool { var invalid Invalid; return errors.As(err, &invalid) }},
		{"missing passphrase", fldcovID("5"), `{"eligible_for_approval":true,"requires_passphrase":true}`, "",
			func(err error) bool { return errors.Is(err, runner.ErrRestorePassphrase) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			digest := f.admit(t, tc.id, tc.operation)
			_, started, err := f.Approve(ctx, tc.id, digest, "root", tc.passphrase)
			if started || !tc.check(err) {
				t.Fatalf("approve: started=%v err=%v", started, err)
			}
			stored, err := f.db.GetRestoreRequest(ctx, tc.id, "")
			if err != nil || stored.Status != "pending" {
				t.Fatalf("request changed: %+v %v", stored, err)
			}
		})
	}

	t.Run("maintenance in progress", func(t *testing.T) {
		id := fldcovID("6")
		digest := f.admit(t, id, `{"eligible_for_approval":true}`)
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, _, err := f.Approve(ctx, id, digest, "root", ""); !errors.Is(err, ErrBusy) {
			t.Fatalf("busy: %v", err)
		}
	})
}

func TestApproveInvalidatesWhenReviewedJobIsGone(t *testing.T) {
	f := newFldcovService(t)
	id := fldcovID("7")
	digest := f.admit(t, id, `{"eligible_for_approval":true,"input":{"job_id":424242,"restore_point_id":1,"item_name":"archive","write_mode":"merge"}}`)
	result, started, err := f.Approve(t.Context(), id, digest, "root", "")
	if started || !errors.Is(err, ErrRequestTerminal) || result.Status != "invalidated" {
		t.Fatalf("missing Job: started=%v status=%q err=%v", started, result.Status, err)
	}
	if result.RunID != 0 {
		t.Fatalf("invalidated request linked run %d", result.RunID)
	}
}

func TestApprovalViewKeepsAuditLinkAfterRunIsDeleted(t *testing.T) {
	f := newFldcovService(t)
	ctx := t.Context()
	id := fldcovID("8")
	digest := f.admit(t, id, `{"eligible_for_approval":true,"item":{"item_name":"archive","item_type":"folder"}}`)
	run := db.JobRun{JobID: f.job, BackupType: "full", ItemsTotal: 1, RunType: "restore", Status: "running"}
	runID, err := f.db.ClaimRestoreApproval(ctx, db.RestoreApproval{RequestID: id, OperatorName: "root", ReviewDigest: digest}, run, time.Now)
	if err != nil || runID == 0 {
		t.Fatalf("claim: %d %v", runID, err)
	}
	if _, err := f.db.Exec(`DELETE FROM job_runs WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	result, started, err := f.Approve(ctx, id, digest, "root", "")
	if err != nil || started || result.RunID != runID || result.Run != nil || result.Approval == nil || result.Approval.OperatorName != "root" {
		t.Fatalf("retry after run deletion: %+v started=%v err=%v", result, started, err)
	}
}

func TestApprovalViewSummarisesRunEvidence(t *testing.T) {
	f := newFldcovService(t)
	ctx := t.Context()
	id := fldcovID("9")
	digest := f.admit(t, id, `{"eligible_for_approval":true,"item":{"item_name":"archive","item_type":"folder"}}`)
	run := db.JobRun{JobID: f.job, BackupType: "full", ItemsTotal: 1, RunType: "restore", Status: "running"}
	runID, err := f.db.ClaimRestoreApproval(ctx, db.RestoreApproval{RequestID: id, OperatorName: "root", ReviewDigest: digest}, run, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	run.ID, run.Status, run.ItemsDone = runID, "completed", 1
	run.Log = `[{"status":"ok","readiness_checks":["` + runner.FolderDataStaged + `","fixture-unlisted-check","` + runner.FolderRestoreCompleted + `"]},{"status":"not_attempted"},{"status":"unknown"}]`
	if err := f.db.FinishApprovedRestore(ctx, id, run, "completed", ""); err != nil {
		t.Fatal(err)
	}
	stored, err := f.db.GetRestoreRequest(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.view(ctx, stored, false, "")
	if err != nil || result.Approval != nil || result.Run == nil {
		t.Fatalf("assistant view: %+v %v", result, err)
	}
	if got := result.Run.ReadinessChecks; len(got) != 2 || got[0] != runner.FolderDataStaged || got[1] != runner.FolderRestoreCompleted {
		t.Fatalf("readiness checks: %v", got)
	}
	if got := result.Run.ItemResults; len(got) != 2 || got[0].Status != "completed" || got[1].Status != "not_attempted" {
		t.Fatalf("item results: %+v", got)
	}

	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.view(context.Background(), stored, true, ""); err == nil {
		t.Fatal("view hid a database failure")
	}
}

func TestReviewMetadataRefusesStaleOrUnneededReviews(t *testing.T) {
	f := newFldcovService(t)
	ctx := t.Context()
	if _, err := f.ReviewMetadata(ctx, "bad", "", "secret"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}
	if _, err := f.ReviewMetadata(ctx, fldcovID("a"), "", ""); !errors.Is(err, runner.ErrRestorePassphrase) {
		t.Fatalf("missing passphrase: %v", err)
	}
	if _, err := f.ReviewMetadata(ctx, fldcovID("a"), "", "secret"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("unknown request: %v", err)
	}

	undecodable := fldcovID("b")
	digest := f.admit(t, undecodable, `{"requires_metadata_review":"yes"}`)
	if _, err := f.ReviewMetadata(ctx, undecodable, "wrong", "secret"); !errors.Is(err, ErrReviewDigest) {
		t.Fatalf("wrong digest: %v", err)
	}
	if _, err := f.ReviewMetadata(ctx, undecodable, digest, "secret"); err == nil || !strings.Contains(err.Error(), "decoding") {
		t.Fatalf("undecodable operation: %v", err)
	}

	unneeded := fldcovID("c")
	digest = f.admit(t, unneeded, `{"eligible_for_approval":true}`)
	var invalid Invalid
	if _, err := f.ReviewMetadata(ctx, unneeded, digest, "secret"); !errors.As(err, &invalid) {
		t.Fatalf("unneeded review: %v", err)
	}

	gone := fldcovID("d")
	digest = f.admit(t, gone, `{"requires_metadata_review":true,"input":{"job_id":424242,"restore_point_id":1,"item_name":"archive","write_mode":"merge"}}`)
	if _, err := f.ReviewMetadata(ctx, gone, digest, "secret"); !errors.Is(err, ErrReviewDigest) {
		t.Fatalf("job removed before review: %v", err)
	}
}

func TestSameMetadataReviewSourcePrefersAnchor(t *testing.T) {
	anchored := Operation{SourceAnchor: "a", SourceDigest: "x"}
	if !sameMetadataReviewSource(anchored, Operation{SourceAnchor: "a", SourceDigest: "y"}) {
		t.Fatal("anchor match was rejected because the digest changed")
	}
	if sameMetadataReviewSource(anchored, Operation{SourceAnchor: "b", SourceDigest: "x"}) {
		t.Fatal("anchor change was accepted")
	}
	if !sameMetadataReviewSource(Operation{SourceDigest: "x"}, Operation{SourceDigest: "x"}) ||
		sameMetadataReviewSource(Operation{SourceDigest: "x"}, Operation{SourceDigest: "y"}) {
		t.Fatal("digest comparison without an anchor is wrong")
	}
}

func TestStopRunReportsUnknownRun(t *testing.T) {
	f := newFldcovService(t)
	if _, err := f.StopRun(t.Context(), 424242, "root"); err == nil {
		t.Fatal("stopping an unknown run succeeded")
	}
}

func TestReviewMetadataRefusesClaimedRequest(t *testing.T) {
	f := newFldcovService(t)
	id := fldcovID("e")
	digest := f.admit(t, id, `{"requires_metadata_review":true}`)
	run := db.JobRun{JobID: f.job, BackupType: "full", ItemsTotal: 1, RunType: "restore", Status: "running"}
	if _, err := f.db.ClaimRestoreApproval(t.Context(), db.RestoreApproval{RequestID: id, OperatorName: "root", ReviewDigest: digest}, run, time.Now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReviewMetadata(t.Context(), id, digest, "secret"); !errors.Is(err, ErrRequestTerminal) {
		t.Fatalf("claimed request: %v", err)
	}
}
