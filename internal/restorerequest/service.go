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

// LegacyMCPCaller is set by the authenticated MCP route for clients without
// an Assistant Key. The legacy identities are used only to submit a request.
type LegacyMCPCaller uint8

const (
	LegacyMCPAPIKey LegacyMCPCaller = iota + 1
	LegacyMCPLocal
)

type Principal struct {
	AssistantDigest string
	LocalReview     bool
	LegacyMCP       LegacyMCPCaller
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
		result.Container = nil
		result.VM = nil
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
	if err != nil {
		return db.AssistantKey{}, fmt.Errorf("loading Assistant Key: %w", err)
	}
	return key, nil
}

func (s *Service) Submit(ctx context.Context, principal Principal, input Input) (View, error) {
	return s.submit(ctx, principal, input, false)
}

func (s *Service) SubmitLegacyMCP(ctx context.Context, principal Principal, input Input) (View, error) {
	return s.submit(ctx, principal, input, true)
}

func (s *Service) submissionOwner(ctx context.Context, principal Principal, legacyMCP bool) (digest, name string, err error) {
	if legacyMCP && principal.AssistantDigest == "" {
		// Reserved identities cannot match a hexadecimal Assistant Key digest.
		switch principal.LegacyMCP {
		case LegacyMCPAPIKey:
			return "mcp:api-key", "MCP API Key", nil
		case LegacyMCPLocal:
			return "mcp:local", "Host-local MCP", nil
		}
	}
	key, err := s.assistant(ctx, principal)
	return principal.AssistantDigest, key.Name, err
}

func (s *Service) submit(ctx context.Context, principal Principal, input Input, legacyMCP bool) (View, error) {
	owner, _, err := s.submissionOwner(ctx, principal, legacyMCP)
	if err != nil {
		return View{}, err
	}
	normalized := normalizedSubmission(input)
	submission, err := json.Marshal(normalized)
	if err != nil {
		return View{}, fmt.Errorf("encoding Restore Request submission: %w", err)
	}
	rawSubmission := submission
	if input != normalized {
		rawSubmission, err = json.Marshal(input)
		if err != nil {
			return View{}, fmt.Errorf("encoding Restore Request submission: %w", err)
		}
	}

	pending, err := s.pendingSubmission(ctx, owner, string(submission), string(rawSubmission))
	if err == nil {
		result, err := view(pending, false, "")
		if err == nil && legacyMCP && result.ManualRestoreRequired {
			return View{}, manualContainerRestore
		}
		result.Reused = true
		return result, err
	}
	if !errors.Is(err, db.ErrNotFound) {
		return View{}, fmt.Errorf("finding a reusable Restore Request: %w", err)
	}

	op, err := s.capture(ctx, normalized, "")
	if err != nil {
		return View{}, err // capture adds its own context
	}
	if legacyMCP && op.ManualRestoreRequired {
		return View{}, manualContainerRestore
	}
	operation, err := json.Marshal(op)
	if err != nil {
		return View{}, fmt.Errorf("encoding Restore Request operation: %w", err)
	}
	if !s.maintenance.TryLock() {
		return View{}, ErrBusy
	}
	defer s.maintenance.Unlock()
	_, name, err := s.submissionOwner(ctx, principal, legacyMCP)
	if err != nil {
		return View{}, err
	}
	var id [16]byte
	rand.Read(id[:]) // crypto/rand.Read never returns an error since Go 1.24.
	now := s.now().UTC()
	request := db.RestoreRequest{
		ID: hex.EncodeToString(id[:]), OwnerDigest: owner,
		RequesterName: name, Operation: operation, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(approvalWindow),
	}
	record, created, err := s.db.AdmitRestoreRequest(ctx, request, string(submission), string(rawSubmission), maxPendingPerKey, maxRecentPerKey, now.Add(-submissionWindow))
	if errors.Is(err, db.ErrRestoreRequestLimit) {
		return View{}, ErrLimit
	}
	if err != nil {
		return View{}, fmt.Errorf("admitting Restore Request: %w", err)
	}
	result, err := view(record, false, s.flushChange(created))
	if err == nil && legacyMCP && result.ManualRestoreRequired {
		return View{}, manualContainerRestore
	}
	result.Reused = !created
	return result, err
}

// pendingSubmission finds a reusable pending request. It holds the maintenance
// lock like every other ledger access so a database replacement in progress
// refuses the call instead of answering from the database being replaced.
// The lock is released before capture, which may probe storage.
func (s *Service) pendingSubmission(ctx context.Context, owner, submission, rawSubmission string) (db.RestoreRequest, error) {
	if !s.maintenance.TryLock() {
		return db.RestoreRequest{}, ErrBusy
	}
	defer s.maintenance.Unlock()
	return s.db.PendingRestoreRequest(ctx, owner, s.now(), submission, rawSubmission)
}

// Canonicalize the default folder type and the spelling of an alternate
// location. Capture still validates the path policy and resolves symlinks.
func normalizedSubmission(input Input) Input {
	if input.ItemType == "folder" {
		input.ItemType = ""
	}
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
		return View{}, fmt.Errorf("loading Restore Request: %w", err)
	}
	warning := ""
	if request.Status == "pending" && !request.ExpiresAt.After(s.now()) {
		warning, err = s.expire(ctx, request.ID)
		if err != nil {
			return View{}, err
		}
		request, err = s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
		if err != nil {
			return View{}, fmt.Errorf("reloading expired Restore Request: %w", err)
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
		return View{}, fmt.Errorf("loading Restore Request to withdraw: %w", err)
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
		return View{}, fmt.Errorf("updating withdrawn Restore Request: %w", err)
	}
	warning := ""
	if expiring {
		warning = s.flushChange(changed)
	} else if changed && flushAfter != 0 {
		s.persistWithdrawal(ctx, &request, now, flushAfter)
	}
	request, err = s.db.GetRestoreRequest(ctx, id, principal.AssistantDigest)
	if err != nil {
		return View{}, fmt.Errorf("reloading withdrawn Restore Request: %w", err)
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
		return Page{}, fmt.Errorf("listing Restore Requests: %w", err)
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
		if err != nil {
			return "", fmt.Errorf("checking for expired Restore Requests: %w", err)
		}
		if !due {
			return "", nil
		}
	}
	if !s.maintenance.TryLock() {
		return "", ErrBusy
	}
	defer s.maintenance.Unlock()
	changed, err := s.db.ExpireRestoreRequests(ctx, now, id)
	if err != nil {
		return "", fmt.Errorf("expiring Restore Requests: %w", err)
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
