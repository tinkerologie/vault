package api

import (
	"strings"
	"testing"
)

// TestRouteDocsCoverage fails if any registered /api/v1 route lacks a
// description, so the Settings → API reference can never drift from the router.
func TestRouteDocsCoverage(t *testing.T) {
	database := testDB(t)
	srv := NewServer(database, ServerConfig{Addr: ":0"})
	var missing []string
	for _, rt := range walkRoutes(srv.router) {
		if rt.Description == "" {
			missing = append(missing, rt.Method+" "+rt.Path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("undocumented routes (add to routeDocs in routedocs.go):\n  %s", strings.Join(missing, "\n  "))
	}
}
