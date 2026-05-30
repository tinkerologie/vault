package anomaly

import (
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
}

// Now returns the current time from the injected clock.
func (e EvalContext) Now() time.Time { return e.Clock.Now() }

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
