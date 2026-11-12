package restorerequest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

const (
	approvalWindow          = 24 * time.Hour
	submissionWindow        = time.Hour
	withdrawalRetryInterval = time.Minute
	maxPendingPerKey        = 5
	maxRecentPerKey         = 6
	persistenceWarning      = "the request state is saved in the working database but could not be saved to flash"
)

var (
	ErrPermission = errors.New("operator access or a permitted Assistant Key is required")
	ErrBusy       = errors.New("database maintenance is in progress")
	ErrLimit      = errors.New("restore request limit reached: follow an existing pending request, withdraw one to free a slot, or retry after the rolling hour")
)

type Principal struct {
	AssistantDigest string
	LocalReview     bool
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFrom(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalKey{}).(Principal)
	return principal
}

type Service struct {
	db                   *db.DB
	runner               *runner.Runner
	revocations          *db.AssistantRevocations
	maintenance          *sync.Mutex
	flush                func() error
	now                  func() time.Time
	withdrawalRetryAfter time.Time // guarded by maintenance
}

func New(database *db.DB, revocations *db.AssistantRevocations, maintenance *sync.Mutex, execution *runner.Runner, flush func() error, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, runner: execution, revocations: revocations, maintenance: maintenance, flush: flush, now: now}
}

type View struct {
	ID                 string        `json:"id"`
	Status             string        `json:"status"`
	CreatedAt          time.Time     `json:"created_at"`
	ExpiresAt          time.Time     `json:"expires_at"`
	TerminalAt         *time.Time    `json:"terminal_at,omitempty"`
	TerminalReason     string        `json:"terminal_reason,omitempty"`
	RequesterName      string        `json:"requester_name"`
	ReviewLink         string        `json:"review_link"`
	ReviewDigest       string        `json:"review_digest,omitempty"`
	ConfiguredPath     string        `json:"configured_path,omitempty"`
	RunID              int64         `json:"run_id,omitempty"`
	Approval           *ApprovalView `json:"approval,omitempty"`
	Run                *RunView      `json:"run,omitempty"`
	ResolvedPath       string        `json:"resolved_path,omitempty"`
	PersistenceWarning string        `json:"persistence_warning,omitempty"`
	Reused             bool          `json:"reused,omitempty"`
	Review
}

type Page struct {
	Requests           []View `json:"requests"`
	NextBefore         string `json:"next_before,omitempty"`
	PersistenceWarning string `json:"persistence_warning,omitempty"`
}

func view(request db.RestoreRequest, operator bool, warning string) (View, error) {
	var op Operation
	if err := json.Unmarshal(request.Operation, &op); err != nil {
		return View{}, fmt.Errorf("decoding reviewed Restore Request operation: %w", err)
	}
	if request.WithdrawalFlushAfter != 0 {
		warning = persistenceWarning
	}
	result := View{
		ID: request.ID, Status: request.Status, CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt,
		TerminalAt: request.TerminalAt, TerminalReason: request.TerminalReason, RequesterName: request.RequesterName,
		ReviewLink: "/plugins/vault/include/app.php#/restore?request=" + request.ID,
		Review:     op.Review, PersistenceWarning: warning,
	}
	if operator {
		result.ResolvedPath = op.ResolvedPath
		result.ConfiguredPath = op.ConfiguredPath
		if op.TargetChoice == "alternate" {
			result.ConfiguredPath = op.Input.Destination
		}
		result.ReviewDigest = reviewDigest(request.Operation)
	} else {
		result.Item = result.Item.Redacted()
		result.JobName = runner.RedactRecoveryLabel(result.JobName)
		result.RequesterName = runner.RedactRecoveryLabel(result.RequesterName)
	}
	return result, nil
}

func (s *Service) assistant(ctx context.Context, principal Principal) (db.AssistantKey, error) {
	digest := principal.AssistantDigest
	if digest == "" || !s.revocations.Permits(digest) {
		return db.AssistantKey{}, ErrPermission
	}
	key, err := s.db.FindAssistantKey(ctx, digest)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !s.revocations.PermitsRestoreRequests(key)) {
		return db.AssistantKey{}, ErrPermission
	}
	return key, err
}

func (s *Service) Submit(ctx context.Context, principal Principal, input Input) (View, error) {
	if _, err := s.assistant(ctx, principal); err != nil {
		return View{}, err
	}
	normalized := normalizedSubmission(input)
	submission, err := json.Marshal(normalized)
	if err != nil {
		return View{}, err
	}
	rawSubmission := submission
	if input.Destination != normalized.Destination {
		rawSubmission, err = json.Marshal(input)
		if err != nil {
			return View{}, err
		}
	}

	pending, err := s.db.PendingRestoreRequest(ctx, principal.AssistantDigest, s.now(), string(submission), string(rawSubmission))
	if err == nil {
		result, err := view(pending, false, "")
		result.Reused = true
		return result, err
	}
	if !errors.Is(err, db.ErrNotFound) {
		return View{}, err
	}

	op, err := Capture(ctx, s.db, normalized)
	if err != nil {
		return View{}, err
	}
	operation, err := json.Marshal(op)
	if err != nil {
		return View{}, err
	}
	if !s.maintenance.TryLock() {
		return View{}, ErrBusy
	}
	defer s.maintenance.Unlock()
	key, err := s.assistant(ctx, principal)
	if err != nil {
		return View{}, err
	}
	var id [16]byte
	rand.Read(id[:])
	now := s.now().UTC()
	request := db.RestoreRequest{
		ID: hex.EncodeToString(id[:]), OwnerDigest: principal.AssistantDigest,
		RequesterName: key.Name, Operation: operation, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(approvalWindow),
	}
	record, created, err := s.db.AdmitRestoreRequest(ctx, request, string(submission), string(rawSubmission), maxPendingPerKey, maxRecentPerKey, now.Add(-submissionWindow))
	if errors.Is(err, db.ErrRestoreRequestLimit) {
		return View{}, ErrLimit
	}
	if err != nil {
		return View{}, err
	}
	result, err := view(record, false, s.flushChange(created))
	result.Reused = !created
	return result, err
}

// Normalize spelling only. Capture validates the path policy and resolves
// symlinks, while the identity stays tied to the location the caller named.
func normalizedSubmission(input Input) Input {
	destination := input.Destination
	if filepath.IsAbs(destination) && safepath.RejectControlChars(destination) == nil &&
		!strings.Contains(destination, "../") && !strings.Contains(destination, `..\`) &&
		!slices.Contains(strings.FieldsFunc(strings.TrimSpace(destination), func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		input.Destination = filepath.Clean(strings.TrimSpace(destination))
	}
	return input
}

func (s *Service) Get(ctx context.Context, principal Principal, id string) (View, error) {
	if !principal.LocalReview || principal.AssistantDigest != "" {
		if _, err := s.assistant(ctx, principal); err != nil {
			return View{}, err
		}
	}
	if !validRequestID(id) {
		return View{}, db.ErrNotFound
	}
	request, err := s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
	if err != nil {
		return View{}, err
	}
	warning := ""
	if request.Status == "pending" && !request.ExpiresAt.After(s.now()) {
		warning, err = s.expire(ctx, request.ID)
		if err != nil {
			return View{}, err
		}
		request, err = s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
		if err != nil {
			return View{}, err
		}
	}
	return s.view(ctx, request, principal.AssistantDigest == "", warning)
}

func (s *Service) Withdraw(ctx context.Context, principal Principal, id string) (View, error) {
	if principal.AssistantDigest == "" {
		return View{}, ErrPermission
	}
	if !s.maintenance.TryLock() {
		return View{}, ErrBusy
	}
	defer s.maintenance.Unlock()
	if _, err := s.assistant(ctx, principal); err != nil {
		return View{}, err
	}
	if !validRequestID(id) {
		return View{}, db.ErrNotFound
	}
	request, err := s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
	if err != nil {
		return View{}, err
	}
	now := s.now().UTC()
	if request.Status != "pending" {
		if request.WithdrawalFlushAfter != 0 {
			s.retryWithdrawalFlush(ctx, &request, now)
		}
		return s.view(ctx, request, false, "")
	}
	var changed bool
	flushAfter := int64(0)
	expiring := !request.ExpiresAt.After(now)
	if expiring {
		changed, err = s.db.ExpireRestoreRequests(ctx, now, id)
	} else {
		if s.flush != nil {
			flushAfter = now.Add(withdrawalRetryInterval).UnixNano()
		}
		changed, err = s.db.WithdrawRestoreRequest(ctx, id, principal.AssistantDigest, now, flushAfter)
	}
	if err != nil {
		return View{}, err
	}
	warning := ""
	if expiring {
		warning = s.flushChange(changed)
	} else if changed && flushAfter != 0 {
		s.persistWithdrawal(ctx, &request, now, flushAfter)
	}
	request, err = s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, request, false, warning)
}

func (s *Service) retryWithdrawalFlush(ctx context.Context, request *db.RestoreRequest, now time.Time) {
	if now.UnixNano() < request.WithdrawalFlushAfter || now.Before(s.withdrawalRetryAfter) {
		return
	}
	next := now.Add(withdrawalRetryInterval).UnixNano()
	reserved, err := s.db.ReserveWithdrawalFlush(ctx, request.ID, request.OwnerDigest, now.UnixNano(), next)
	if err != nil || !reserved {
		return
	}
	request.WithdrawalFlushAfter = next
	s.persistWithdrawal(ctx, request, now, next)
}

func (s *Service) persistWithdrawal(ctx context.Context, request *db.RestoreRequest, now time.Time, flushAfter int64) {
	s.withdrawalRetryAfter = now.Add(withdrawalRetryInterval)
	if s.flushChange(true) != "" {
		return
	}
	// The flash copy already contains the withdrawal. Mark it confirmed
	// in the working ledger without forcing a second snapshot.
	cleared, err := s.db.MarkWithdrawalFlushed(context.WithoutCancel(ctx), request.ID, flushAfter)
	if err == nil && cleared {
		request.WithdrawalFlushAfter = 0
	}
}

func (s *Service) List(ctx context.Context, principal Principal, statuses []string, before string, limit int) (Page, error) {
	if !principal.LocalReview || principal.AssistantDigest != "" {
		return Page{}, ErrPermission
	}
	if len(statuses) == 0 {
		statuses = []string{"pending"}
	}
	for _, status := range statuses {
		switch status {
		case "pending", "withdrawn", "expired", "invalidated", "running", "completed", "failed", "partial", "interrupted", "cancelled":
		default:
			return Page{}, Invalid("invalid Restore Request status")
		}
	}
	if limit < 0 || limit > 200 || (before != "" && !validRequestID(before)) {
		return Page{}, Invalid("invalid Restore Request pagination")
	}
	if limit == 0 {
		limit = 50
	}
	warning, err := s.expire(ctx, "")
	if err != nil {
		return Page{}, err
	}
	requests, err := s.db.ListRestoreRequests(ctx, statuses, before, limit+1)
	if err != nil {
		return Page{}, err
	}
	count := min(len(requests), limit)
	result := Page{Requests: make([]View, 0, count), PersistenceWarning: warning}
	for _, request := range requests[:count] {
		entry, err := s.view(ctx, request, true, "")
		if err != nil {
			return Page{}, err
		}
		result.Requests = append(result.Requests, entry)
	}
	if count < len(requests) {
		result.NextBefore = requests[count-1].ID
	}
	return result, nil
}

func (s *Service) ReconcileExpiry(ctx context.Context) error {
	warning, err := s.expire(ctx, "")
	if err == nil && warning != "" {
		err = errors.New(warning)
	}
	return err
}

func (s *Service) expire(ctx context.Context, id string) (string, error) {
	now := s.now().UTC()
	if id == "" {
		due, err := s.db.HasExpiredRestoreRequests(ctx, now)
		if err != nil || !due {
			return "", err
		}
	}
	if !s.maintenance.TryLock() {
		return "", ErrBusy
	}
	defer s.maintenance.Unlock()
	changed, err := s.db.ExpireRestoreRequests(ctx, now, id)
	if err != nil {
		return "", err
	}
	return s.flushChange(changed), nil
}

func (s *Service) flushChange(changed bool) string {
	if changed && s.flush != nil {
		if err := s.flush(); err != nil {
			log.Printf("restore requests: could not persist the working database to flash")
			return persistenceWarning
		}
	}
	return ""
}
