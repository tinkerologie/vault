package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

type RestoreRequestHandler struct {
	service *restorerequest.Service
}

func NewRestoreRequestHandler(service *restorerequest.Service) *RestoreRequestHandler {
	return &RestoreRequestHandler{service: service}
}

func (h *RestoreRequestHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var input restorerequest.Input
	if !decodeRestoreRequest(w, r, &input) {
		return
	}
	ctx := r.Context()
	result, err := h.service.Submit(ctx, restorerequest.PrincipalFrom(ctx), input)
	respondRestoreRequest(w, http.StatusCreated, result, err)
}

func (h *RestoreRequestHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	result, err := h.service.Get(ctx, restorerequest.PrincipalFrom(ctx), chi.URLParam(r, "id"))
	respondRestoreRequest(w, http.StatusOK, result, err)
}

func (h *RestoreRequestHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	result, err := h.service.Withdraw(ctx, restorerequest.PrincipalFrom(ctx), chi.URLParam(r, "id"))
	respondRestoreRequest(w, http.StatusOK, result, err)
}

func (h *RestoreRequestHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	var statuses []string
	if raw := query.Get("status"); raw != "" {
		statuses = strings.Split(raw, ",")
	}
	var limit int
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			respondError(w, http.StatusBadRequest, "invalid Restore Request pagination")
			return
		}
	}
	result, err := h.service.List(ctx, restorerequest.PrincipalFrom(ctx), statuses, query.Get("before"), limit)
	respondRestoreRequest(w, http.StatusOK, result, err)
}

// Strict decoding keeps secrets, selective file restores and caller-supplied
// identities outside the request contract.
func decodeRestoreRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid request body or unsupported fields")
		return false
	}
	return true
}

func respondRestoreRequest(w http.ResponseWriter, status int, result any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	var validation restorerequest.Invalid
	switch {
	case err == nil:
		respondJSON(w, status, result)
	case errors.As(err, &validation):
		respondError(w, http.StatusBadRequest, validation.Error())
	case errors.Is(err, restorerequest.ErrPermission):
		respondError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, db.ErrNotFound):
		respondError(w, http.StatusNotFound, "Restore Request or selected Restore Point not found")
	case errors.Is(err, restorerequest.ErrBusy):
		respondError(w, http.StatusConflict, err.Error())
	default:
		respondInternalError(w, err)
	}
}
