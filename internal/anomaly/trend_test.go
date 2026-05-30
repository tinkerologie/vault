package anomaly

import (
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestEvaluateTrendDetectors_CapacityAnomaly seeds a storage destination with
// >= 14 declining capacity samples, calls EvaluateTrendDetectors(), and asserts
// that a capacity anomaly was persisted to the database and broadcast to the hub.
//
// This test is deterministic: all time values are driven by FakeClock.
func TestEvaluateTrendDetectors_CapacityAnomaly(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}

	// Register only the capacity trajectory trend detector.
	reg := &Registry{}
	reg.Register(NewCapacityTrajectoryDetector(d))

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)

	// Create a storage destination.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "trend-dest",
		Type:   "local",
		Config: `{"path":"/tmp/trend"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	// Seed 20 declining capacity samples spread over 20 days (oldest first).
	// Total = 1 TiB; free starts at 22 GB and drains 1 GB/day.
	// At sample[19]: free = 22e9 - 19*1e9 = 3e9 bytes.
	// OLS slope ≈ -1 GB/day; ETA ≈ 3 days → critical (< warnDays/2 = 7).
	t0 := clk.Now().AddDate(0, 0, -20)
	const total = int64(1 << 40)
	for i := 0; i < 20; i++ {
		sampledAt := t0.Add(time.Duration(i) * 24 * time.Hour)
		freeBytes := int64(22e9) - int64(i)*int64(1e9)
		if freeBytes < 0 {
			freeBytes = 0
		}
		if err := d.InsertCapacitySample(db.CapacitySample{
			DestID:     destID,
			SampledAt:  sampledAt,
			TotalBytes: total,
			FreeBytes:  freeBytes,
		}); err != nil {
			t.Fatalf("InsertCapacitySample(i=%d): %v", i, err)
		}
	}

	// Run trend evaluation (synchronous — no goroutine needed).
	ev.EvaluateTrendDetectors()

	// Assert: at least one capacity anomaly was persisted for this destination.
	fp := Fingerprint("capacity_trajectory", ScopeDestination, destID, "free_bytes_eta_days")
	row, err := d.GetOpenAnomalyByFingerprint(fp)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v — capacity anomaly was not persisted", err)
	}
	if row.ScopeID != destID {
		t.Errorf("anomaly.ScopeID = %d, want %d", row.ScopeID, destID)
	}
	if row.Severity != string(SeverityCritical) {
		t.Errorf("anomaly.Severity = %q, want %q", row.Severity, SeverityCritical)
	}

	// Assert: hub received an anomaly.raised broadcast.
	msgs := hub.messages()
	found := false
	for _, m := range msgs {
		if containsAnomaly(m, "anomaly.raised") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hub did not receive anomaly.raised; broadcast count=%d", len(msgs))
	}
}

// TestEvaluateTrendDetectors_NoDestinations verifies that EvaluateTrendDetectors
// is a no-op (no panic, no error) when no storage destinations exist.
func TestEvaluateTrendDetectors_NoDestinations(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	clk := NewFakeClock(time.Now())
	hub := &recordingBroadcaster{}

	reg := &Registry{}
	reg.Register(NewCapacityTrajectoryDetector(d))

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)

	// Should complete without panic or error even with zero destinations.
	ev.EvaluateTrendDetectors()

	if len(hub.messages()) != 0 {
		t.Errorf("expected no broadcasts with no destinations; got %d", len(hub.messages()))
	}
}

// TestEvaluateTrendDetectors_NoTrendDetectors verifies that the method is a
// no-op when only KindPerRun detectors are registered.
func TestEvaluateTrendDetectors_NoTrendDetectors(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	clk := NewFakeClock(time.Now())
	hub := &recordingBroadcaster{}

	reg := &Registry{}
	// Only register per-run detectors.
	reg.Register(NewSizeDriftDetector())
	reg.Register(NewDurationDriftDetector())

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)

	// Create a destination so there's something to iterate over.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "no-trend-dest",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	_ = destID

	// Must be a no-op: no trend detectors → returns early.
	ev.EvaluateTrendDetectors()

	if len(hub.messages()) != 0 {
		t.Errorf("expected no broadcasts with no trend detectors; got %d", len(hub.messages()))
	}
}

// TestEvaluateTrendDetectors_PanicIsolation verifies that a panicking trend
// detector does not prevent other trend detectors from running.
func TestEvaluateTrendDetectors_PanicIsolation(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	logBuf := captureLog(t)

	// Register a KindTrend panicking detector followed by the real capacity
	// detector. The panic must not prevent the capacity detector from running.
	reg := &Registry{}
	reg.Register(&trendPanicDetector{name: "trend_panicky"})
	reg.Register(NewCapacityTrajectoryDetector(d))

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)

	// Create a destination with enough declining samples.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "panic-dest",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	t0 := clk.Now().AddDate(0, 0, -20)
	for i := 0; i < 20; i++ {
		sampledAt := t0.Add(time.Duration(i) * 24 * time.Hour)
		freeBytes := int64(22e9) - int64(i)*int64(1e9)
		if freeBytes < 0 {
			freeBytes = 0
		}
		_ = d.InsertCapacitySample(db.CapacitySample{
			DestID:     destID,
			SampledAt:  sampledAt,
			TotalBytes: 1 << 40,
			FreeBytes:  freeBytes,
		})
	}

	// Must not panic.
	ev.EvaluateTrendDetectors()

	// The panic recovery should be logged.
	if logs := logBuf.String(); !strings.Contains(logs, "trend_panicky") {
		t.Errorf("expected panic log for trend_panicky; log output: %s", logs)
	}

	// The capacity detector should still have run and persisted an anomaly.
	fp := Fingerprint("capacity_trajectory", ScopeDestination, destID, "free_bytes_eta_days")
	if _, err := d.GetOpenAnomalyByFingerprint(fp); err != nil {
		t.Errorf("capacity anomaly not persisted after panic isolation: %v", err)
	}

}

// trendPanicDetector is a KindTrend detector that panics in Evaluate.
type trendPanicDetector struct{ name string }

func (p *trendPanicDetector) Name() string { return p.name }
func (p *trendPanicDetector) Kind() Kind   { return KindTrend }
func (p *trendPanicDetector) Evaluate(_ EvalContext) ([]Anomaly, error) {
	panic("intentional panic in trend detector " + p.name)
}

// containsAnomaly reports whether the raw JSON message has the given event type.
func containsAnomaly(msg []byte, eventType string) bool {
	return strings.Contains(string(msg), `"`+eventType+`"`)
}
