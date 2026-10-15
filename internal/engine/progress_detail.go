package engine

import "context"

// Phase names the kind of work an item is doing, so progress can say
// "Uploading 84.2 GB / 225 GB" instead of a percentage of fixed workflow
// stages (#456).
type Phase string

// Progress phases, in the order an item normally passes through them.
const (
	PhaseScanning     Phase = "scanning"     // walking the source to size it
	PhasePreparing    Phase = "preparing"    // stopping services, snapshots, staging setup
	PhaseProcessing   Phase = "processing"   // archiving, chunking, hashing, extracting
	PhaseTransferring Phase = "transferring" // moving bytes to or from storage
	PhaseVerifying    Phase = "verifying"    // reading data back to check it
	PhaseFinalizing   Phase = "finalizing"   // manifests, restore points, restarts
)

// ProgressDetail is a structured progress report for the current item. A
// zero total means the total is unknown, which the UI shows as
// indeterminate progress rather than an invented percentage. Done counters
// are cumulative within the phase.
type ProgressDetail struct {
	Phase      Phase
	BytesDone  int64
	BytesTotal int64
	FilesDone  int64
	FilesTotal int64
}

// DetailReporter receives structured progress for one item.
type DetailReporter func(item string, d ProgressDetail)

type detailReporterKey struct{}

// WithDetailReporter attaches fn to ctx. The runner installs one per run;
// engines report through ReportDetail.
func WithDetailReporter(ctx context.Context, fn DetailReporter) context.Context {
	return context.WithValue(ctx, detailReporterKey{}, fn)
}

// ReportDetail sends d for item to the reporter on ctx, if any. It is a
// best-effort side channel next to ProgressFunc and a no-op without a
// reporter, so engines can call it unconditionally.
func ReportDetail(ctx context.Context, item string, d ProgressDetail) {
	if fn, ok := ctx.Value(detailReporterKey{}).(DetailReporter); ok && fn != nil {
		fn(item, d)
	}
}
