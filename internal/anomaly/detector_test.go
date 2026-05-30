package anomaly

import (
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// stubDetector is a test double that satisfies the Detector interface.
type stubDetector struct {
	name string
	kind Kind
	out  []Anomaly
	err  error
}

func (s stubDetector) Name() string                            { return s.name }
func (s stubDetector) Kind() Kind                              { return s.kind }
func (s stubDetector) Evaluate(EvalContext) ([]Anomaly, error) { return s.out, s.err }

// --- Registry tests ---

func TestDetectorRegistry_PerRunFiltersCorrectly(t *testing.T) {
	var r Registry
	r.Register(stubDetector{name: "run1", kind: KindPerRun})
	r.Register(stubDetector{name: "trend1", kind: KindTrend})
	r.Register(stubDetector{name: "run2", kind: KindPerRun})

	got := r.PerRun()
	if len(got) != 2 {
		t.Fatalf("PerRun: want 2 detectors, got %d", len(got))
	}
	for _, d := range got {
		if d.Kind() != KindPerRun {
			t.Errorf("PerRun: unexpected Kind %v for detector %q", d.Kind(), d.Name())
		}
	}
}

func TestDetectorRegistry_TrendFiltersCorrectly(t *testing.T) {
	var r Registry
	r.Register(stubDetector{name: "run1", kind: KindPerRun})
	r.Register(stubDetector{name: "trend1", kind: KindTrend})
	r.Register(stubDetector{name: "trend2", kind: KindTrend})

	got := r.Trend()
	if len(got) != 2 {
		t.Fatalf("Trend: want 2 detectors, got %d", len(got))
	}
	for _, d := range got {
		if d.Kind() != KindTrend {
			t.Errorf("Trend: unexpected Kind %v for detector %q", d.Kind(), d.Name())
		}
	}
}

func TestDetectorRegistry_MixedRegistrationFiltersCorrectly(t *testing.T) {
	var r Registry
	r.Register(stubDetector{name: "r1", kind: KindPerRun})
	r.Register(stubDetector{name: "t1", kind: KindTrend})
	r.Register(stubDetector{name: "r2", kind: KindPerRun})
	r.Register(stubDetector{name: "t2", kind: KindTrend})
	r.Register(stubDetector{name: "r3", kind: KindPerRun})

	perRun := r.PerRun()
	trend := r.Trend()

	if len(perRun) != 3 {
		t.Errorf("PerRun: want 3, got %d", len(perRun))
	}
	if len(trend) != 2 {
		t.Errorf("Trend: want 2, got %d", len(trend))
	}
}

func TestDetectorRegistry_EmptyRegistryReturnsNonNilSlices(t *testing.T) {
	var r Registry

	perRun := r.PerRun()
	if perRun == nil {
		t.Error("PerRun: expected non-nil slice for empty registry")
	}
	if len(perRun) != 0 {
		t.Errorf("PerRun: expected empty slice, got len %d", len(perRun))
	}

	trend := r.Trend()
	if trend == nil {
		t.Error("Trend: expected non-nil slice for empty registry")
	}
	if len(trend) != 0 {
		t.Errorf("Trend: expected empty slice, got len %d", len(trend))
	}
}

// --- EvalContext.Now tests ---

func TestDetectorEvalContextNow(t *testing.T) {
	want := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	clk := NewFakeClock(want)
	ctx := EvalContext{Clock: clk}

	got := ctx.Now()
	if !got.Equal(want) {
		t.Errorf("Now(): want %v, got %v", want, got)
	}
}

// --- EvalContext.Sensitivity tests ---

func TestDetectorEvalContextSensitivity_JobOverride(t *testing.T) {
	job := &db.Job{AnomalySensitivity: "strict"}
	ctx := EvalContext{
		Job:               job,
		GlobalSensitivity: "balanced",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensStrict {
		t.Errorf("Sensitivity(): want SensStrict, got %v", got)
	}
}

func TestDetectorEvalContextSensitivity_DestinationOverride(t *testing.T) {
	dest := &db.StorageDestination{AnomalySensitivity: "permissive"}
	ctx := EvalContext{
		Destination:       dest,
		GlobalSensitivity: "strict",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensPermissive {
		t.Errorf("Sensitivity(): want SensPermissive, got %v", got)
	}
}

func TestDetectorEvalContextSensitivity_GlobalDefault(t *testing.T) {
	ctx := EvalContext{
		GlobalSensitivity: "permissive",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensPermissive {
		t.Errorf("Sensitivity(): want SensPermissive, got %v", got)
	}
}

func TestDetectorEvalContextSensitivity_JobOverrideWinsOverDestination(t *testing.T) {
	// Job takes priority in the switch (first case wins).
	job := &db.Job{AnomalySensitivity: "strict"}
	dest := &db.StorageDestination{AnomalySensitivity: "permissive"}
	ctx := EvalContext{
		Job:               job,
		Destination:       dest,
		GlobalSensitivity: "balanced",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensStrict {
		t.Errorf("Sensitivity(): want SensStrict (job wins), got %v", got)
	}
}

func TestDetectorEvalContextSensitivity_EmptyOverrideFallsBackToGlobal(t *testing.T) {
	job := &db.Job{AnomalySensitivity: ""}
	ctx := EvalContext{
		Job:               job,
		GlobalSensitivity: "balanced",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensBalanced {
		t.Errorf("Sensitivity(): want SensBalanced, got %v", got)
	}
}

func TestDetectorEvalContextSensitivity_UnknownValueFallsBackToBalanced(t *testing.T) {
	ctx := EvalContext{
		GlobalSensitivity: "unknown-value",
		Clock:             NewFakeClock(time.Now()),
	}

	got := ctx.Sensitivity()
	if got != SensBalanced {
		t.Errorf("Sensitivity(): want SensBalanced for unknown value, got %v", got)
	}
}
