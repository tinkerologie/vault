package anomaly

import (
	"log"
	"strings"

	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/notify"
)

// AnomalyNotifier is the interface the Evaluator calls to dispatch anomaly
// notifications. The real implementation wraps the package-level notify
// functions (Unraid + Discord). Tests inject a recording fake.
//
// Send is called with the anomaly data needed to build a notification payload.
// The call is always synchronous from the Evaluator's perspective; the
// implementation may spawn a goroutine internally (e.g. for Discord HTTP).
type AnomalyNotifier interface {
	// SendAnomaly dispatches a notification for the given anomaly.
	// isUpdate is true when the anomaly was already open (escalation path).
	SendAnomaly(a Anomaly, scopeName string, isUpdate bool)
}

// realNotifier is the production AnomalyNotifier. It reads the Discord
// webhook URL from the DB at Send time (via the closure) so hot-reload of
// settings works without restarting the daemon.
type realNotifier struct {
	// webhookURL returns the current Discord webhook URL (may be empty → skipped).
	webhookURL func() string
}

// NewRealNotifier returns an AnomalyNotifier that dispatches Unraid + Discord
// notifications using the package-level notify functions. webhookURL is called
// on every Send so settings changes take effect immediately.
//
// webhookURL must be non-nil; to disable Discord dispatch entirely pass
// func() string { return "" }. The nil guard inside SendAnomaly is retained as
// defensive code but callers should not rely on it.
func NewRealNotifier(webhookURL func() string) AnomalyNotifier {
	return &realNotifier{webhookURL: webhookURL}
}

func (r *realNotifier) SendAnomaly(a Anomaly, scopeName string, isUpdate bool) {
	sev := string(a.Severity)

	// Unraid notification.
	if err := notify.SendAnomalyUnraid(a.Summary, a.Details, sev); err != nil {
		log.Printf("WARN anomaly: unraid notify: %v", err)
	}

	// Discord notification — only when a webhook URL is configured.
	if r.webhookURL != nil {
		if url := r.webhookURL(); url != "" {
			embed := notify.BuildAnomalyEmbed(notify.AnomalyEmbedParams{
				Severity:  sev,
				ScopeKind: string(a.ScopeKind),
				ScopeName: scopeName,
				Summary:   a.Summary,
				Details:   a.Details,
				IsUpdate:  isUpdate,
			})
			// Fire-and-forget, matching the runner's existing backup-notification
			// pattern (see runner.sendNotification). The caller (maybeNotify)
			// stamps notified_at synchronously BEFORE this goroutine is spawned,
			// so dedup is guaranteed even if this async Discord HTTP call is slow,
			// fails, or is abandoned at daemon shutdown. We intentionally do not
			// track this goroutine with a WaitGroup.
			go func() {
				if err := notify.SendDiscord(url, embed); err != nil {
					log.Printf("WARN anomaly: discord notify: %v", err)
				}
			}()
		}
	}
}

// SetNotifier injects an AnomalyNotifier into the Evaluator. A nil notifier is
// a no-op (safe for tests and deployments that don't configure notifications).
//
// It MUST be called after NewEvaluator and before Start(), during the
// synchronous daemon startup sequence — mirroring the SetEvaluator /
// SetAnomalyEvaluator convention. It is NOT safe for concurrent runtime
// reconfiguration: the worker goroutine reads e.notifier without a lock, so
// mutating it after Start() races with maybeNotify.
func (e *Evaluator) SetNotifier(n AnomalyNotifier) {
	e.notifier = n
}

// maybeNotify dispatches an anomaly notification subject to three gates:
//  1. notifier == nil → skip (no-op; tests/daemon without notifier configured).
//  2. a.NotifiedAt != nil → skip (already notified; dedup).
//  3. Severity gate: a.Severity must be >= global min-severity setting
//     ("anomaly_notify_min_severity", default "critical") OR the anomaly's
//     job has a per-job override in its notify_on field (tokens: "anomaly:any",
//     "anomaly:<severity>").
//
// On a decision to send, this calls notifier.SendAnomaly and stamps
// notified_at via db.MarkAnomalyNotified so subsequent calls are deduped.
func (e *Evaluator) maybeNotify(a Anomaly, isUpdate bool) {
	if e.notifier == nil {
		return
	}
	// Dedup: already notified.
	if a.NotifiedAt != nil {
		return
	}

	// Severity gate.
	minSevDefault := docsmeta.DefaultFor("anomaly_notify_min_severity")
	minSevStr, err := e.db.GetSetting("anomaly_notify_min_severity", minSevDefault)
	if err != nil {
		minSevStr = minSevDefault
	}
	shouldSend := severityAtLeast(a.Severity, Severity(minSevStr))

	// Per-job override (only meaningful when scope is a job).
	if !shouldSend && a.ScopeKind == ScopeJob {
		job, err := e.db.GetJob(a.ScopeID)
		if err == nil {
			shouldSend = jobHasAnomalyOverride(job.NotifyOn, a.Severity)
		}
	}

	if !shouldSend {
		return
	}

	// Without a real DB id we cannot stamp notified_at, so a duplicate send on
	// the next observation is possible. Skip rather than accept the risk.
	if a.ID == 0 {
		log.Printf("WARN anomaly: maybeNotify: skipping send for anomaly with id=0 (fingerprint %q)", a.Fingerprint)
		return
	}

	// Resolve a human-readable scope name for the notification payload.
	scopeName := resolveAnomalyScopeName(e, a)

	// Dispatch.
	e.notifier.SendAnomaly(a, scopeName, isUpdate)

	// Stamp notified_at to prevent re-sends.
	if err := e.db.MarkAnomalyNotified(a.ID, e.clock.Now()); err != nil {
		log.Printf("WARN anomaly: MarkAnomalyNotified(%d): %v", a.ID, err)
	}
}

// severityAtLeast returns true when candidate is greater than or equal to min
// in the ordering info < warning < critical.
func severityAtLeast(candidate, min Severity) bool {
	return severityRank(candidate) >= severityRank(min)
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 0
	default:
		// Unknown severity strings rank below info so they don't trigger sends.
		return -1
	}
}

// jobHasAnomalyOverride returns true when the notifyOn CSV contains a token
// that force-enables anomaly notifications for the given severity. The existing
// "always" / "failure" / "never" tokens are unrelated to anomalies and are
// intentionally ignored here.
//
// Recognised tokens:
//   - "anomaly:any"      → matches any severity
//   - "anomaly:critical" → matches severity == critical
//   - "anomaly:warning"  → matches severity == warning or critical
//   - "anomaly:info"     → matches any severity (same as any)
func jobHasAnomalyOverride(notifyOn string, sev Severity) bool {
	for _, token := range strings.Split(notifyOn, ",") {
		token = strings.TrimSpace(token)
		switch token {
		case "anomaly:any", "anomaly:info":
			return true
		case "anomaly:warning":
			// warning or above.
			if sev == SeverityWarning || sev == SeverityCritical {
				return true
			}
		case "anomaly:critical":
			if sev == SeverityCritical {
				return true
			}
		}
	}
	return false
}

// resolveAnomalyScopeName looks up a human-readable name for the anomaly's
// scope. Returns an empty string on lookup failure (non-fatal).
func resolveAnomalyScopeName(e *Evaluator, a Anomaly) string {
	switch a.ScopeKind {
	case ScopeJob:
		job, err := e.db.GetJob(a.ScopeID)
		if err != nil {
			return ""
		}
		return job.Name
	case ScopeDestination:
		dest, err := e.db.GetStorageDestination(a.ScopeID)
		if err != nil {
			return ""
		}
		return dest.Name
	default:
		return ""
	}
}
