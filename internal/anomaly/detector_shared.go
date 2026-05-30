package anomaly

import "math"

// evaluateHighSide applies Rules A (MAD z-score) and B (median multiplier) to
// detect high-side anomalies. It returns the dominant Severity and the z-score.
//
//   - Rule A (MAD z-score): |z| > k → warning; |z| >= 2k → critical.
//     Disabled when mad == 0 (avoids ±Inf z-scores).
//   - Rule B (median multiplier): observed > multiplier * median → warning.
//
// Returns ("", 0) when neither rule fires (no signal).
// zA is always finite: 0 when mad==0, otherwise the real modified z-score.
//
// Precondition: callers must handle low-side (shrinkage) anomalies before
// calling this; evaluateHighSide uses |z| and will fire Rule A even when
// observed < median.
func evaluateHighSide(observed, median, mad, k, multiplier float64) (severity Severity, zA float64) {
	// Rule A: modified z-score (disabled when mad == 0).
	if mad != 0 {
		zA = ModifiedZScore(observed, median, mad)
		absZ := math.Abs(zA)
		switch {
		case absZ >= 2*k:
			severity = SeverityCritical
		case absZ > k:
			severity = SeverityWarning
		}
	}

	// Rule B: median multiplier (growth).
	var severityB Severity
	if observed > multiplier*median {
		severityB = SeverityWarning
	}

	// Dominant severity: take the higher of the two rules.
	severity = higherSeverity(severity, severityB)
	return severity, zA
}
