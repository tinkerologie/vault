package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

func (h *RestoreRequestHandler) StopRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	runID, ok := parseID(w, r, "runID")
	if !ok {
		return
	}
	var envelope operatorproof.Envelope
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		respondRestoreRequest(w, 0, nil, restorerequest.ErrPermission)
		return
	}
	action, ok := h.proof.Verify(envelope, "stop", strconv.FormatInt(runID, 10))
	if !ok || action.ReviewDigest != "" || action.Passphrase != "" {
		respondRestoreRequest(w, 0, nil, restorerequest.ErrPermission)
		return
	}
	result, err := h.service.StopRun(r.Context(), runID, action.Operator)
	switch {
	case errors.Is(err, db.ErrNotFound):
		respondError(w, http.StatusNotFound, "Approved Restore Run not found")
	case err != nil:
		respondInternalError(w, err)
	case result.Status == "stopping":
		respondJSON(w, http.StatusAccepted, result)
	default:
		respondJSON(w, http.StatusConflict, result)
	}
}
