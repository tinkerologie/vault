package engine

import (
	"context"
	"testing"
)

func TestReportDetail(t *testing.T) {
	// No reporter: a no-op, never a panic.
	ReportDetail(context.Background(), "x", ProgressDetail{Phase: PhaseScanning})

	var gotItem string
	var got ProgressDetail
	ctx := WithDetailReporter(t.Context(), func(item string, d ProgressDetail) { gotItem, got = item, d })
	ReportDetail(ctx, "appdata", ProgressDetail{Phase: PhaseTransferring, BytesDone: 5, BytesTotal: 10})
	if gotItem != "appdata" || got.Phase != PhaseTransferring || got.BytesDone != 5 || got.BytesTotal != 10 {
		t.Fatalf("reporter got %q %+v", gotItem, got)
	}

	// A nil reporter stored on the context is ignored.
	ReportDetail(WithDetailReporter(t.Context(), nil), "x", ProgressDetail{})
}
