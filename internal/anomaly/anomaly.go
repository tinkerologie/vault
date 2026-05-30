// Package anomaly defines the core types, converters, and tuning parameters for Vault's anomaly detection subsystem.
package anomaly

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type State string

const (
	StateOpen         State = "open"
	StateResolved     State = "resolved"
	StateAcknowledged State = "acknowledged"
	StateExpected     State = "expected"
)

type AckAction string

const (
	AckDismiss      AckAction = "dismiss"
	AckMarkExpected AckAction = "mark_expected"
)

type Kind int

const (
	KindPerRun Kind = iota
	KindTrend
)

type ScopeKind string

const (
	ScopeJob         ScopeKind = "job"
	ScopeDestination ScopeKind = "destination"
)

// Fingerprint returns a stable sha256 hex identity for an anomaly signal.
func Fingerprint(detector string, scope ScopeKind, scopeID int64, metric string) string {
	h := sha256.New()
	// Length-prefix each string field so boundaries are unambiguous even
	// when detector/scope/metric themselves contain the '|' separator.
	fmt.Fprintf(h, "%d:%s|%d:%s|%d|%d:%s",
		len(detector), detector,
		len(string(scope)), scope,
		scopeID,
		len(metric), metric)
	return hex.EncodeToString(h.Sum(nil))
}

// Anomaly mirrors db.Anomaly but uses typed enums. Kept separate so the
// detector layer doesn't leak raw string columns; converted at the db boundary.
type Anomaly struct {
	ID             int64
	Fingerprint    string
	Detector       string
	Severity       Severity
	ScopeKind      ScopeKind
	ScopeID        int64
	Metric         string
	Observed       float64
	Expected       *float64
	Deviation      *float64
	JobRunID       *int64
	Summary        string
	Details        string
	State          State
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	ResolvedAt     *time.Time
	AcknowledgedAt *time.Time
	AckAction      AckAction
	AckBy          string
	AckReason      string
	NotifiedAt     *time.Time
}

// ToDB converts an anomaly.Anomaly to a db.Anomaly for persistence.
func ToDB(a Anomaly) db.Anomaly {
	return db.Anomaly{
		ID:             a.ID,
		Fingerprint:    a.Fingerprint,
		Detector:       a.Detector,
		Severity:       string(a.Severity),
		ScopeKind:      string(a.ScopeKind),
		ScopeID:        a.ScopeID,
		Metric:         a.Metric,
		Observed:       a.Observed,
		Expected:       a.Expected,
		Deviation:      a.Deviation,
		JobRunID:       a.JobRunID,
		Summary:        a.Summary,
		Details:        a.Details,
		State:          string(a.State),
		FirstSeenAt:    a.FirstSeenAt,
		LastSeenAt:     a.LastSeenAt,
		ResolvedAt:     a.ResolvedAt,
		AcknowledgedAt: a.AcknowledgedAt,
		AckAction:      string(a.AckAction),
		AckBy:          a.AckBy,
		AckReason:      a.AckReason,
		NotifiedAt:     a.NotifiedAt,
	}
}

// FromDB converts a db.Anomaly to an anomaly.Anomaly with typed enums.
func FromDB(r db.Anomaly) Anomaly {
	return Anomaly{
		ID:             r.ID,
		Fingerprint:    r.Fingerprint,
		Detector:       r.Detector,
		Severity:       Severity(r.Severity),
		ScopeKind:      ScopeKind(r.ScopeKind),
		ScopeID:        r.ScopeID,
		Metric:         r.Metric,
		Observed:       r.Observed,
		Expected:       r.Expected,
		Deviation:      r.Deviation,
		JobRunID:       r.JobRunID,
		Summary:        r.Summary,
		Details:        r.Details,
		State:          State(r.State),
		FirstSeenAt:    r.FirstSeenAt,
		LastSeenAt:     r.LastSeenAt,
		ResolvedAt:     r.ResolvedAt,
		AcknowledgedAt: r.AcknowledgedAt,
		AckAction:      AckAction(r.AckAction),
		AckBy:          r.AckBy,
		AckReason:      r.AckReason,
		NotifiedAt:     r.NotifiedAt,
	}
}
