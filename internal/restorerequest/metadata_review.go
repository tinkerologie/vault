package restorerequest

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

// ReviewMetadata unlocks the backup evidence for a signed-in operator. The
// ledger gets the reviewed effects and fingerprints, never the passphrase.
func (s *Service) ReviewMetadata(ctx context.Context, id, digest, passphrase string) (View, error) {
	if !validRequestID(id) {
		return View{}, db.ErrNotFound
	}
	if passphrase == "" {
		return View{}, runner.ErrRestorePassphrase
	}
	current, err := s.db.GetRestoreRequest(ctx, id, "")
	if err != nil {
		return View{}, err
	}
	if digest != reviewDigest(current.Operation) {
		return View{}, ErrReviewDigest
	}
	if current.Status != "pending" || !current.ExpiresAt.After(s.now()) {
		return View{}, ErrRequestTerminal
	}
	var stored Operation
	if err := json.Unmarshal(current.Operation, &stored); err != nil {
		return View{}, err
	}
	if !stored.RequiresMetadataReview {
		return View{}, Invalid("this Restore Request does not need a metadata review")
	}

	fresh, err := s.capture(ctx, stored.Input, passphrase)
	if err == nil && fresh.sourceProbeUnavailable() {
		return View{}, runner.ErrApprovedStorageUnavailable
	}
	var invalid Invalid
	if errors.Is(err, runner.ErrReviewedRestoreChanged) || errors.Is(err, db.ErrNotFound) ||
		errors.As(err, &invalid) || (err == nil && !sameMetadataReviewSource(stored, fresh)) {
		return View{}, ErrReviewDigest
	}
	if err != nil {
		return View{}, err
	}
	data, err := json.Marshal(fresh)
	if err != nil {
		return View{}, err
	}
	if !s.maintenance.TryLock() {
		return View{}, ErrBusy
	}
	defer s.maintenance.Unlock()
	changed, err := s.db.ReviewRestoreRequestMetadata(ctx, id, current.Operation, data, s.now().UTC())
	if err != nil {
		return View{}, err
	}
	if !changed {
		return View{}, ErrReviewDigest
	}
	current.Operation = data
	return s.view(ctx, current, true, s.flushChange(true))
}

func sameMetadataReviewSource(stored, fresh Operation) bool {
	if stored.SourceAnchor != "" {
		return fresh.SourceAnchor == stored.SourceAnchor
	}
	return fresh.SourceDigest == stored.SourceDigest
}
