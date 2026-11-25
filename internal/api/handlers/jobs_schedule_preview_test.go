package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSchedulePreview(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	tests := []struct {
		name     string
		query    string
		wantCode int
		wantRuns int
	}{
		{"default count", "schedule=" + url.QueryEscape("15 2 * * *"), http.StatusOK, 3},
		{"explicit count", "schedule=" + url.QueryEscape("0 2 L * *") + "&count=2", http.StatusOK, 2},
		{"manual only", "schedule=", http.StatusOK, 0},
		{"invalid schedule", "schedule=" + url.QueryEscape("not a cron"), http.StatusBadRequest, 0},
		{"bad count", "schedule=" + url.QueryEscape("0 2 * * *") + "&count=zero", http.StatusBadRequest, 0},
		{"negative count", "schedule=" + url.QueryEscape("0 2 * * *") + "&count=-1", http.StatusBadRequest, 0},
		{"too long", "schedule=" + strings.Repeat("1", maxSchedulePreviewLen+1), http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			h.SchedulePreview(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/schedule-preview?"+tt.query, nil))
			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var resp SchedulePreviewResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Runs) != tt.wantRuns {
				t.Fatalf("runs = %v, want %d", resp.Runs, tt.wantRuns)
			}
			if resp.Runs == nil {
				t.Fatal("runs must encode as [], not null")
			}
			if resp.Timezone == "" || len(resp.UTCOffset) != len("+00:00") {
				t.Fatalf("timezone = %q, offset = %q", resp.Timezone, resp.UTCOffset)
			}
			for _, r := range resp.Runs {
				ts, err := time.Parse(time.RFC3339, r)
				if err != nil {
					t.Fatalf("run %q is not RFC 3339: %v", r, err)
				}
				if tt.name == "default count" && ts.Minute() != 15 {
					t.Fatalf("run %q lost its minute", r)
				}
			}
		})
	}
}
