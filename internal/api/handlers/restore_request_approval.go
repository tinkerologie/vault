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
		respondError(w, http.StatusConflict, sentinelMessage(err, runner.ErrRunnerBusy, runner.ErrRunnerDraining))
		return
	case errors.Is(err, restorerequest.ErrApprovalPersistence), errors.Is(err, runner.ErrApprovedStorageUnavailable):
		respondError(w, http.StatusServiceUnavailable, sentinelMessage(err, restorerequest.ErrApprovalPersistence, runner.ErrApprovedStorageUnavailable))
		return
	case errors.Is(err, runner.ErrRestorePassphrase):
		respondError(w, http.StatusBadRequest, runner.ErrRestorePassphrase.Error())
		return
	case errors.Is(err, restorerequest.ErrReviewDigest):
		respondError(w, http.StatusConflict, restorerequest.ErrReviewDigest.Error())
		return
	case errors.Is(err, restorerequest.ErrRequestTerminal):
		respondJSON(w, http.StatusGone, result)
		return
	case started:
		status = http.StatusAccepted
	}
	respondRestoreRequest(w, status, result, err)
}

func (h *RestoreRequestHandler) ReviewMetadata(w http.ResponseWriter, r *http.Request) {
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
	action, ok := h.proof.Verify(envelope, "review", id)
	if !ok {
		respondRestoreRequest(w, 0, nil, restorerequest.ErrPermission)
		return
	}
	result, err := h.service.ReviewMetadata(r.Context(), id, action.ReviewDigest, action.Passphrase)
	switch {
	case errors.Is(err, runner.ErrRunnerBusy), errors.Is(err, restorerequest.ErrReviewDigest):
		respondError(w, http.StatusConflict, sentinelMessage(err, runner.ErrRunnerBusy, restorerequest.ErrReviewDigest))
	case errors.Is(err, runner.ErrApprovedStorageUnavailable):
		respondError(w, http.StatusServiceUnavailable, runner.ErrApprovedStorageUnavailable.Error())
	case errors.Is(err, runner.ErrRestorePassphrase):
		respondError(w, http.StatusBadRequest, runner.ErrRestorePassphrase.Error())
	case errors.Is(err, restorerequest.ErrRequestTerminal):
		respondError(w, http.StatusGone, restorerequest.ErrRequestTerminal.Error())
	default:
		respondRestoreRequest(w, http.StatusOK, result, err)
	}
}

// sentinelMessage returns the fixed text of the matching sentinel, so context
// added while the error was wrapped never reaches the client. The caller has
// already matched one of them; the last one is the fallback.
func sentinelMessage(err error, first, second error) string {
	if errors.Is(err, first) {
		return first.Error()
	}
	return second.Error()
}
