package handlers

import (
	"encoding/json"
	"log"
	"net/http"
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

// ConfigChangeHook is called after a handler mutates persistent configuration
// (jobs, storage destinations, settings). It triggers an immediate USB flash
// backup so the flash copy always has fresh data.
type ConfigChangeHook func()
