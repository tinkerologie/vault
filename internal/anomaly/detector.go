package anomaly

import (
	"math"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// Detector is the interface every anomaly detector must implement.
type Detector interface {
	Name() string
	Kind() Kind
	Evaluate(ctx EvalContext) ([]Anomaly, error)
}

// EvalContext is the read-only snapshot a detector evaluates against.
// JobRun is nil for trend detectors; Destination is set for capacity trend.
//
// Detectors must treat all fields as read-only and must not append to or
// mutate RecentRuns or CapacitySamples — the slices share backing storage
// with the caller.
type EvalContext struct {
	JobRun            *db.JobRun
	Job               *db.Job
	Destination       *db.StorageDestination
	RecentRuns        []db.JobRun
	Baseline          *db.JobBaseline
	CapacitySamples   []db.CapacitySample
	GlobalSensitivity string
	Clock             Clock

	// floorLookup is set by evaluator.buildContext. It returns the maximum
	// observed value across all "expected"-state rows for a fingerprint.
	// Nil when not wired (e.g. in tests that construct EvalContext directly).
	floorLookup func(fingerprint string) float64
}

// Now returns the current time from the injected clock.
func (e EvalContext) Now() time.Time { return e.Clock.Now() }

// ExpectedFloor returns the maximum observed value the user has marked
// "expected" for this fingerprint (via AckAction=mark_expected). Returns 0 if
// no such rows exist or if the floor lookup is not wired (safe for tests that
// construct EvalContext directly without a db handle).
func (e EvalContext) ExpectedFloor(fingerprint string) float64 {
	if e.floorLookup == nil {
		return 0
	}
	return e.floorLookup(fingerprint)
}

// ApplyFloor raises baseThreshold to at least (floor + k*mad) when a user has
// previously marked an anomaly for this fingerprint as "expected". If no floor
// exists (floor == 0) or the floor-adjusted value is lower, baseThreshold is
// returned unchanged.
//
// Detectors call this so that re-occurrence of an acknowledged anomaly is not
// re-raised until the observed value exceeds the prior expected level.
func (e EvalContext) ApplyFloor(fingerprint string, baseThreshold, k, mad float64) float64 {
	floor := e.ExpectedFloor(fingerprint)
	if floor <= 0 {
		return baseThreshold
	}
	return math.Max(baseThreshold, floor+k*mad)
}

// Sensitivity resolves the effective sensitivity from the per-scope override
// (job or destination) layered over the global default.
func (e EvalContext) Sensitivity() Sensitivity {
	var override string
	switch {
	case e.Job != nil:
		override = e.Job.AnomalySensitivity
	case e.Destination != nil:
		override = e.Destination.AnomalySensitivity
	}
	return Resolve(override, e.GlobalSensitivity)
}

// Registry holds registered detectors and allows filtering by Kind.
type Registry struct {
	detectors []Detector
}

// Register adds a detector to the registry.
func (r *Registry) Register(d Detector) { r.detectors = append(r.detectors, d) }

// PerRun returns all registered detectors with Kind == KindPerRun.
func (r *Registry) PerRun() []Detector {
	out := make([]Detector, 0, len(r.detectors))
	for _, d := range r.detectors {
		if d.Kind() == KindPerRun {
			out = append(out, d)
		}
	}
	return out
}

// Trend returns all registered detectors with Kind == KindTrend.
func (r *Registry) Trend() []Detector {
	out := make([]Detector, 0, len(r.detectors))
	for _, d := range r.detectors {
		if d.Kind() == KindTrend {
			out = append(out, d)
		}
	}
	return out
}
