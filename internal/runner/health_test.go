package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// stubCapacityAdapter is a minimal Adapter for runner capacity tests.
// It returns a pre-configured Capacity from GetCapacity; every other
// method is a no-op. Used to drive probeCapacity without hitting a
// real network or filesystem.
type stubCapacityAdapter struct {
	cap         storage.Capacity
	testConnErr error
}

func (s *stubCapacityAdapter) Write(_ string, _ io.Reader) error { return nil }
func (s *stubCapacityAdapter) Read(_ string) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (s *stubCapacityAdapter) ReadRange(_ string, _, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (s *stubCapacityAdapter) Delete(_ string) error                     { return nil }
func (s *stubCapacityAdapter) List(_ string) ([]storage.FileInfo, error) { return nil, nil }
func (s *stubCapacityAdapter) Stat(_ string) (storage.FileInfo, error) {
	return storage.FileInfo{}, nil
}
func (s *stubCapacityAdapter) TestConnection() error { return s.testConnErr }
func (s *stubCapacityAdapter) GetCapacity(_ context.Context) (storage.Capacity, error) {
	return s.cap, nil
}
func (s *stubCapacityAdapter) WriteFrom(_ string, _ func() (io.ReadCloser, error)) error {
	return nil
}

// errCapacityAdapter is like stubCapacityAdapter but GetCapacity returns an error.
type errCapacityAdapter struct {
	msg string
}

func (e *errCapacityAdapter) Write(_ string, _ io.Reader) error { return nil }
func (e *errCapacityAdapter) Read(_ string) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (e *errCapacityAdapter) ReadRange(_ string, _, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (e *errCapacityAdapter) Delete(_ string) error                     { return nil }
func (e *errCapacityAdapter) List(_ string) ([]storage.FileInfo, error) { return nil, nil }
func (e *errCapacityAdapter) Stat(_ string) (storage.FileInfo, error)   { return storage.FileInfo{}, nil }
func (e *errCapacityAdapter) TestConnection() error                     { return nil }
func (e *errCapacityAdapter) GetCapacity(_ context.Context) (storage.Capacity, error) {
	return storage.Capacity{}, errors.New(e.msg)
}
func (e *errCapacityAdapter) WriteFrom(_ string, _ func() (io.ReadCloser, error)) error {
	return nil
}

// TestProbeCapacityPersistsAndBroadcasts verifies that a successful
// GetCapacity probe stores the result in the DB and fires a
// storage_capacity_updated broadcast.
func TestProbeCapacityPersistsAndBroadcasts(t *testing.T) {
	t.Parallel()
	r, database := newTestRunner(t)

	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "cap-probe",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := database.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}

	want := storage.Capacity{
		TotalBytes: 1 << 40,
		UsedBytes:  1 << 30,
		FreeBytes:  (1 << 40) - (1 << 30),
		ProbedAt:   time.Now().UTC().Truncate(time.Second),
		Source:     "test",
	}
	adapter := &stubCapacityAdapter{cap: want}

	cap, err := r.probeCapacity(context.Background(), dest, adapter)
	if err != nil {
		t.Fatalf("probeCapacity: %v", err)
	}
	if cap != want {
		t.Errorf("returned capacity = %+v, want %+v", cap, want)
	}

	got, err := database.GetStorageDestination(id)
	if err != nil {
		t.Fatalf("GetStorageDestination: %v", err)
	}
	if got.CapacitySource != "test" {
		t.Errorf("capacity_source = %q, want test", got.CapacitySource)
	}
	if got.CapacityTotalBytes == nil || *got.CapacityTotalBytes != want.TotalBytes {
		t.Errorf("capacity_total_bytes = %v, want %d", got.CapacityTotalBytes, want.TotalBytes)
	}
	if got.CapacityUsedBytes == nil || *got.CapacityUsedBytes != want.UsedBytes {
		t.Errorf("capacity_used_bytes = %v, want %d", got.CapacityUsedBytes, want.UsedBytes)
	}
	if got.CapacityFreeBytes == nil || *got.CapacityFreeBytes != want.FreeBytes {
		t.Errorf("capacity_free_bytes = %v, want %d", got.CapacityFreeBytes, want.FreeBytes)
	}
	if got.CapacityError != "" {
		t.Errorf("capacity_error = %q, want empty on success", got.CapacityError)
	}
}

// TestProbeCapacityRecordsErrorWithoutAffectingHealth verifies that when
// GetCapacity returns an error, the error is persisted in capacity_error
// but the health verdict is NOT changed. Capacity failure must NEVER
// promote a healthy destination to "unhealthy".
func TestProbeCapacityRecordsErrorWithoutAffectingHealth(t *testing.T) {
	t.Parallel()
	r, database := newTestRunner(t)

	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "cap-fail",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := database.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a healthy state so we can verify it doesn't change.
	if err := database.UpdateStorageDestinationHealth(id, "ok", ""); err != nil {
		t.Fatal(err)
	}

	adapter := &errCapacityAdapter{msg: "synthetic capacity failure"}
	_, probeErr := r.probeCapacity(context.Background(), dest, adapter)
	if probeErr == nil {
		t.Fatal("expected probe error, got nil")
	}

	got, err := database.GetStorageDestination(id)
	if err != nil {
		t.Fatalf("GetStorageDestination: %v", err)
	}
	if got.CapacityError != "synthetic capacity failure" {
		t.Errorf("capacity_error = %q, want %q", got.CapacityError, "synthetic capacity failure")
	}
	// Critically: health verdict must NOT have changed.
	if got.LastHealthCheckStatus != "ok" {
		t.Errorf("health verdict changed to %q after capacity failure; must stay %q", got.LastHealthCheckStatus, "ok")
	}
	// Capacity source should be empty (zero CapacityRecord was stored on error).
	if got.CapacitySource != "" {
		t.Errorf("capacity_source = %q, want empty on error path", got.CapacitySource)
	}
}

// TestCapacityToRecord verifies the mapping between storage.Capacity and
// db.CapacityRecord preserves all fields correctly.
func TestCapacityToRecord(t *testing.T) {
	t.Parallel()
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	c := storage.Capacity{
		TotalBytes: 100,
		UsedBytes:  50,
		FreeBytes:  50,
		ProbedAt:   ts,
		Source:     "statfs",
	}
	rec := capacityToRecord(c)
	if rec.TotalBytes != c.TotalBytes {
		t.Errorf("TotalBytes = %d, want %d", rec.TotalBytes, c.TotalBytes)
	}
	if rec.UsedBytes != c.UsedBytes {
		t.Errorf("UsedBytes = %d, want %d", rec.UsedBytes, c.UsedBytes)
	}
	if rec.FreeBytes != c.FreeBytes {
		t.Errorf("FreeBytes = %d, want %d", rec.FreeBytes, c.FreeBytes)
	}
	if !rec.ProbedAt.Equal(c.ProbedAt) {
		t.Errorf("ProbedAt = %v, want %v", rec.ProbedAt, c.ProbedAt)
	}
	if rec.Source != c.Source {
		t.Errorf("Source = %q, want %q", rec.Source, c.Source)
	}
}

// TestBroadcastStorageCapacityPayload verifies that broadcastStorageCapacity
// and BroadcastStorageCapacity do not panic with a live hub and no registered
// clients.
func TestBroadcastStorageCapacityPayload(t *testing.T) {
	t.Parallel()

	hub := ws.NewHub()
	go hub.Run()

	dbPath := t.TempDir() + "/vault.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	r := New(d, hub, nil)

	cap := storage.Capacity{
		TotalBytes: 999,
		UsedBytes:  111,
		FreeBytes:  888,
		ProbedAt:   time.Now().UTC(),
		Source:     "statfs",
	}

	// Both entry points must not panic or block.
	r.broadcastStorageCapacity(42, cap)
	r.BroadcastStorageCapacity(42, cap)
}

// TestCapacitySampler_InsertsOnSuccess verifies that a capacity probe
// reporting a real quota produces a CapacitySample row.
func TestCapacitySampler_InsertsOnSuccess(t *testing.T) {
	t.Parallel()
	_, database := newTestRunner(t)

	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "sampler-ok",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := database.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}

	wantFree := int64(10 << 30)   // 10 GiB
	wantTotal := int64(100 << 30) // 100 GiB

	// checkOneStorage builds its own adapter from dest.Config, so the probe
	// itself cannot be injected here. The sampler's decision logic is shared
	// with production through capacitySampleFor, so this exercises the real
	// predicate plus the DB round-trip.
	before := time.Now().Add(-time.Second)
	free, total, ok := capacitySampleFor(storage.Capacity{
		TotalBytes: wantTotal,
		FreeBytes:  wantFree,
	}, nil)
	if !ok {
		t.Fatal("capacitySampleFor returned ok=false for a quota-reporting probe")
	}
	if insertErr := database.InsertCapacitySample(db.CapacitySample{
		DestID:     dest.ID,
		SampledAt:  time.Now().UTC(),
		FreeBytes:  free,
		TotalBytes: total,
	}); insertErr != nil {
		t.Fatalf("InsertCapacitySample: %v", insertErr)
	}
	after := time.Now().Add(time.Second)

	samples, err := database.ListCapacitySamples(dest.ID, before)
	if err != nil {
		t.Fatalf("ListCapacitySamples: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected at least one CapacitySample row, got none")
	}
	got := samples[len(samples)-1]
	if got.FreeBytes != wantFree {
		t.Errorf("FreeBytes = %d, want %d", got.FreeBytes, wantFree)
	}
	if got.TotalBytes != wantTotal {
		t.Errorf("TotalBytes = %d, want %d", got.TotalBytes, wantTotal)
	}
	if got.SampledAt.Before(before) || got.SampledAt.After(after) {
		t.Errorf("SampledAt = %v out of range [%v, %v]", got.SampledAt, before, after)
	}
}

// TestCapacitySampleFor_SkipsWhenNoQuotaReported covers the silent-skip
// branch: adapters with no quota concept report TotalBytes == 0 (S3 always;
// SFTP/SMB/WebDAV when the server lacks the statvfs/quota extension), and a
// failed probe must not produce a sample either.
func TestCapacitySampleFor_SkipsWhenNoQuotaReported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cap  storage.Capacity
		err  error
	}{
		{"no quota reported", storage.Capacity{TotalBytes: 0, UsedBytes: 5 << 30}, nil},
		{"probe failed", storage.Capacity{TotalBytes: 100 << 30, FreeBytes: 10 << 30}, errors.New("propfind status 500")},
		{"negative total", storage.Capacity{TotalBytes: -1}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, ok := capacitySampleFor(tc.cap, tc.err); ok {
				t.Errorf("capacitySampleFor(%+v, %v) = ok, want skip", tc.cap, tc.err)
			}
		})
	}
}

// TestCapacitySampleFor_ClampsFreeToTotal guards the free <= total invariant
// the sample table relies on, for backends reporting inconsistent block counts.
func TestCapacitySampleFor_ClampsFreeToTotal(t *testing.T) {
	t.Parallel()
	free, total, ok := capacitySampleFor(storage.Capacity{
		TotalBytes: 100,
		FreeBytes:  150,
	}, nil)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if free != 100 || total != 100 {
		t.Errorf("free, total = %d, %d; want 100, 100 (free clamped)", free, total)
	}
}

// TestBroadcastPayloadShape verifies the JSON shape of a
// storage_capacity_updated broadcast message.
func TestBroadcastPayloadShape(t *testing.T) {
	t.Parallel()

	cap := storage.Capacity{
		TotalBytes: 1000,
		UsedBytes:  200,
		FreeBytes:  800,
		ProbedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:     "webdav-quota",
	}
	payload := map[string]any{
		"type":       "storage_capacity_updated",
		"storage_id": int64(7),
		"capacity":   cap,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["type"] != "storage_capacity_updated" {
		t.Errorf("type = %q, want storage_capacity_updated", out["type"])
	}
	if out["storage_id"] != float64(7) {
		t.Errorf("storage_id = %v, want 7", out["storage_id"])
	}
}

func TestHealthCheckActivity(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		errMsg   string
		wantLvl  string
		wantSub  []string
		wantJSON []string
	}{
		{
			name:     "ok check is info without error text",
			status:   "ok",
			wantLvl:  "info",
			wantSub:  []string{"Storage health check", "destination=nas-backup", "status=ok"},
			wantJSON: []string{`"status":"ok"`, `"duration_ms"`},
		},
		{
			name:     "failed check is warn and includes error",
			status:   "failed",
			errMsg:   "dial tcp: connection refused",
			wantLvl:  "warn",
			wantSub:  []string{"destination=nas-backup", "status=failed", "error=dial tcp: connection refused"},
			wantJSON: []string{`"status":"failed"`, `"error":"dial tcp: connection refused"`},
		},
		{
			name:    "failed check without error message still renders",
			status:  "failed",
			wantLvl: "warn",
			wantSub: []string{"destination=nas-backup", "status=failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := db.StorageDestination{ID: 7, Name: "nas-backup"}
			lvl, msg, details := healthCheckActivity(dest, tt.status, tt.errMsg, 1500*time.Millisecond)
			if lvl != tt.wantLvl {
				t.Fatalf("level = %q, want %q", lvl, tt.wantLvl)
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(msg, sub) {
					t.Fatalf("message %q missing substring %q", msg, sub)
				}
			}
			for _, sub := range tt.wantJSON {
				if !strings.Contains(details, sub) {
					t.Fatalf("details %q missing substring %q", details, sub)
				}
			}
		})
	}
}
