package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

type AssistantKeyHandler struct {
	db          *db.DB
	revocations *db.AssistantRevocations
	maintenance *sync.Mutex
	onChange    ConfigChangeHook
	onRevoke    func(int64)
}

func NewAssistantKeyHandler(database *db.DB, revocations *db.AssistantRevocations, maintenance *sync.Mutex, onChange ConfigChangeHook) *AssistantKeyHandler {
	return &AssistantKeyHandler{db: database, revocations: revocations, maintenance: maintenance, onChange: onChange}
}

func (h *AssistantKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	keys, err := h.db.ListAssistantKeys(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not list Assistant Keys")
		return
	}
	respondJSON(w, http.StatusOK, keys)
}

func (h *AssistantKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respondError(w, http.StatusBadRequest, "invalid Assistant Key name")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
		strings.Contains(name, crypto.AssistantKeyPrefix) {
		respondError(w, http.StatusBadRequest, "enter an Assistant Key name of 1 to 100 characters")
		return
	}
	if !h.maintenance.TryLock() {
		respondError(w, http.StatusConflict, "database maintenance is in progress")
		return
	}
	defer h.maintenance.Unlock()
	if !h.revocations.Ready() {
		respondError(w, http.StatusInternalServerError, "Assistant Key revocation ledger unavailable")
		return
	}
	key, err := crypto.GenerateAssistantKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not generate Assistant Key")
		return
	}
	digest, _ := crypto.AssistantKeyDigest(key)
	record, err := h.db.CreateAssistantKey(r.Context(), name, digest)
	if db.IsUniqueViolation(err) {
		respondError(w, http.StatusConflict, "an Assistant Key with this name already exists")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create Assistant Key")
		return
	}

	h.onChange()
	w.Header().Set("Cache-Control", "no-store")
	respondJSON(w, http.StatusCreated, struct {
		db.AssistantKey
		Key string `json:"assistant_key"`
	}{AssistantKey: record, Key: key})
}

func (h *AssistantKeyHandler) SetRevokeHook(fn func(int64)) {
	h.onRevoke = fn
}

func (h *AssistantKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if !h.maintenance.TryLock() {
		respondError(w, http.StatusConflict, "database maintenance is in progress")
		return
	}
	defer h.maintenance.Unlock()
	ctx := r.Context()
	digest, err := h.db.AssistantKeyDigest(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		respondError(w, http.StatusNotFound, "Assistant Key not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not read Assistant Key")
		return
	}
	if err := h.revocations.Revoke(digest); err != nil {
		respondError(w, http.StatusInternalServerError, "could not persist Assistant Key revocation")
		return
	}
	// The ledger is authoritative once written. Close sessions even when a
	// failed deletion leaves the row available for a cleanup retry.
	deleteErr := h.db.DeleteAssistantKey(ctx, id)
	if h.onRevoke != nil {
		h.onRevoke(id)
	}
	if deleteErr != nil {
		respondError(w, http.StatusInternalServerError, "Assistant Key revoked but could not remove its record")
		return
	}
	h.onChange()
	w.WriteHeader(http.StatusNoContent)
}
