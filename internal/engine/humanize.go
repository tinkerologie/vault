package engine

import "github.com/ruaan-deysel/vault/internal/format"

// humanizeBytes renders a byte count for operator-facing progress messages
// (issue #133).
//
// The implementation lives in internal/format so notifications, anomaly
// summaries and engine progress all render a size identically. Kept as a
// local alias because it is called throughout this package.
func humanizeBytes(b float64) string { return format.Bytes(b) }
