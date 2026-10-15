package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnomalyList458(t *testing.T) {
	t.Parallel()
	h, d := newAnomalyHandler(t)
	seedAnomaly(t, d)

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/anomalies?scope_id=1&scope_kind=job", nil))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "test anomaly") {
		t.Errorf("body = %s", w.Body.String())
	}

	breakTable458(t, d, "anomalies")
	w = httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/anomalies", nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestAnomalyGetAndAck458_QueryErrors(t *testing.T) {
	t.Parallel()
	h, d := newAnomalyHandler(t)
	breakTable458(t, d, "anomalies")

	w := httptest.NewRecorder()
	h.Get(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)

	w = httptest.NewRecorder()
	h.Ack(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"action":"dismiss"}`)), "id", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestAnomalyAck458_BadIDAndWriteErrors(t *testing.T) {
	t.Parallel()
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	w := httptest.NewRecorder()
	h.Ack(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", "bad"))
	wantStatus458(t, w, http.StatusBadRequest)

	failWrites458(t, d, "anomalies", "UPDATE")
	w = httptest.NewRecorder()
	h.Ack(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"action":"dismiss"}`)), "id", idStr(id)))
	wantStatus458(t, w, http.StatusInternalServerError)

	w = httptest.NewRecorder()
	h.AckBulk(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"ids":[`+idStr(id)+`],"action":"dismiss"}`)))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestAnomalyGetBaseline458_Errors(t *testing.T) {
	t.Parallel()

	t.Run("job query error", func(t *testing.T) {
		t.Parallel()
		h, d := newAnomalyHandler(t)
		breakTable458(t, d, "jobs")
		w := httptest.NewRecorder()
		h.GetBaseline(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("baseline query error", func(t *testing.T) {
		t.Parallel()
		h, d := newAnomalyHandler(t)
		jobID := seedJob(t, d)
		breakTable458(t, d, "job_baselines")
		w := httptest.NewRecorder()
		h.GetBaseline(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestAnomalyGetTrajectory458_QueryError(t *testing.T) {
	t.Parallel()
	h, d := newAnomalyHandler(t)
	breakTable458(t, d, "destination_capacity_samples")
	w := httptest.NewRecorder()
	h.GetTrajectory(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)
}
