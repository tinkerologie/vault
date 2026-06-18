package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

type apiRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

// walkRoutes enumerates the live router, keeping only /api/v1 routes (prefix
// stripped) and skipping the ignore-set. Shared by the handler and the test so
// the Settings API reference can never drift from the router.
func walkRoutes(router chi.Routes) []apiRoute {
	var out []apiRoute
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1") {
			return nil
		}
		path := strings.TrimSuffix(strings.TrimPrefix(route, "/api/v1"), "/")
		if path == "" {
			return nil
		}
		key := method + " " + path
		if routeDocsIgnore[key] {
			return nil
		}
		out = append(out, apiRoute{Method: method, Path: path, Description: routeDocs[key]})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func (s *Server) handleMetaRoutes(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, walkRoutes(s.router))
}
