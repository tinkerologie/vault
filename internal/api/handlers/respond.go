package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Warning: failed to write JSON response: %v", err)
	}
}

// respondError writes a JSON error response with the given status code and message.
func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, map[string]string{"error": msg})
}

// respondInternalError logs the real error server-side and returns a generic
// message to the client, preventing internal details from leaking in responses.
func respondInternalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	respondError(w, http.StatusInternalServerError, "internal server error")
}

// parseID extracts and validates a numeric URL parameter. It returns the
// parsed int64 and true on success, or writes a 400 error response and
// returns 0, false on failure.
func parseID(w http.ResponseWriter, r *http.Request, param string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, param), 10, 64)
	if err != nil || id <= 0 {
		respondError(w, http.StatusBadRequest, "invalid "+param)
		return 0, false
	}
	return id, true
}

// ConfigChangeHook is called after a handler mutates persistent configuration
// (jobs, storage destinations, settings). It triggers an immediate USB flash
// backup so the flash copy always has fresh data.
type ConfigChangeHook func()
