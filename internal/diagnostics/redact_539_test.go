package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestCollectRedactsRunsActivityAndURLs(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if _, err := d.CreateStorageDestination(db.StorageDestination{Name: "dav", Type: "webdav",
		Config: `{"url":"https://bob:urlpass@dav.example/","nested":[{"endpoint":"s3://tok@host/"}]}`}); err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	itemLog := []map[string]any{{
		"name":     "plex",
		"settings": `{"password":"run-secret","list":[{"api_key":"nested-secret"}]}`,
		"error":    "dial https://bob:errpass@dav.example/ failed: api_key=errkey",
	}}
	raw, err := json.Marshal(itemLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateJobRun(db.JobRun{ID: runID, Status: "failed", Log: string(raw)}); err != nil {
		t.Fatal(err)
	}
	d.LogActivity("error", "storage", "login https://bob:actpass@dav.example/ failed", `{"password":"act-secret"}`)

	bundle, err := NewCollector(d, func() RunnerStatus { return RunnerStatus{} }, "v", nil).Collect()
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"run-secret", "nested-secret", "errpass", "errkey", "urlpass", "tok@", "actpass", "act-secret"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("%q in bundle", leak)
		}
	}
	if len(bundle.Runs) != 1 || len(bundle.Runs[0].ErrorMessages) != 1 || !strings.HasPrefix(bundle.Runs[0].ErrorMessages[0], "plex: ") {
		t.Fatalf("run errors lost: %+v", bundle.Runs)
	}
}

func TestRedactRunLogPlainTextAndFallbacks(t *testing.T) {
	t.Parallel()
	if got := RedactRunLog("Pre-flight failed: password=abc123"); strings.Contains(got, "abc123") {
		t.Errorf("plain text: %s", got)
	}
	if got := RedactRunLog(""); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := RedactURL("http://bob:pw@[::1"); strings.Contains(got, "pw") {
		t.Errorf("unparseable URL leaked: %s", got)
	}
	if got := RedactText("https://bare-token@host/x"); strings.Contains(got, "bare-token") {
		t.Errorf("token-only URL leaked: %s", got)
	}
	if got := RedactJSON(`{"a":"[not json"}`); !strings.Contains(got, "[not json") {
		t.Errorf("string resembling JSON should survive: %s", got)
	}
}

func TestRedactNestedUnderSensitiveKeysAndQueryCredentials(t *testing.T) {
	t.Parallel()
	got := RedactJSON(`{"password":{"value":"nested-secret"},"tokens":["list-secret"],"keep_last":5,` +
		`"url":"https://dav.example/x?sig=sig-secret&signature=two-secret&X-Amz-Signature=three-secret&keep=yes"}`)
	for _, leak := range []string{"nested-secret", "list-secret", "sig-secret", "two-secret", "three-secret"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q in %s", leak, got)
		}
	}
	if !strings.Contains(got, "keep=yes") || !strings.Contains(got, `"keep_last":5`) {
		t.Errorf("harmless values lost: %s", got)
	}
}
