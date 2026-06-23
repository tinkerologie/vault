package db

import "strings"

// IsUniqueViolation reports whether err is a SQLite UNIQUE constraint
// violation. The modernc.org/sqlite driver surfaces these as the canonical
// SQLite message "UNIQUE constraint failed: <table>.<column>". Handlers map
// this to HTTP 409 Conflict instead of a generic 500 so that duplicate-name
// creates and updates return an actionable error to the client.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
