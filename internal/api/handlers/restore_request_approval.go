package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/tinkerologie/vault/internal/runner"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/operatorproof"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

// RestoreApprovalWriteTimeout leaves time for the storage and flash checks
// while staying below the native PHP proxy timeout.
const RestoreApprovalWriteTimeout = 120 * time.Second

func (h *RestoreRequestHandler) Approve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.proof == nil {
		respondRestoreRequest(w, 0, nil, restorerequest.ErrPermission)
		return
	}
	var envelope operatorproof.Envelope
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !decodeRestoreRequest(w, r, &envelope) {
		return
	}
	id := chi.URLParam(r, "id")
	action, ok := h.proof.Verify(envelope, "approve", id)
	if !ok {
		respondRestoreRequest(w, 0, nil, restorerequest.ErrPermission)
		return
	}
	result, started, err := h.service.Approve(r.Context(), id, action.ReviewDigest, action.Operator, action.Passphrase)
	status := http.StatusOK
	switch {
	case errors.Is(err, runner.ErrRunnerBusy), errors.Is(err, runner.ErrRunnerDraining):
		respondError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, restorerequest.ErrApprovalPersistence), errors.Is(err, runner.ErrApprovedStorageUnavailable):
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, runner.ErrRestorePassphrase):
		respondError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, restorerequest.ErrReviewDigest):
		respondError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, restorerequest.ErrRequestTerminal):
		respondJSON(w, http.StatusGone, result)
		return
	case started:
		status = http.StatusAccepted
	}
	respondRestoreRequest(w, status, result, err)
}
