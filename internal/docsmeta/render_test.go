package docsmeta

import (
	"strings"
	"testing"
)

func TestRenderAppSettingsExcludesInternal(t *testing.T) {
	out := RenderAppSettings()

	if strings.Contains(out, "api_key_hash") {
		t.Errorf("RenderAppSettings must not expose internal key api_key_hash")
	}
	if !strings.Contains(out, "history_retention_days") {
		t.Errorf("RenderAppSettings should include real key history_retention_days")
	}
	if !strings.Contains(out, "| Setting | Type | Default | Description |") {
		t.Errorf("RenderAppSettings should render the table header line")
	}
	if !strings.Contains(out, "## General") {
		t.Errorf("RenderAppSettings should render a General group heading")
	}
}

// Job is a minimal stand-in used to exercise RenderStruct's reflection in a
// hermetic unit test, without depending on the full db.Job layout. Its type
// name "Job" makes rt.Name()+"."+Field match the real FieldDocs /
// InternalFields keys ("Job.ID", "Job.Schedule").
type Job struct {
	ID       string `json:"id"`       // in InternalFields -> must be omitted
	Schedule string `json:"schedule"` // documented -> must be rendered
	secret   string //nolint:unused // exercises the unexported-field skip
}

func TestRenderStructSkipsInternalFields(t *testing.T) {
	out := RenderStruct("Job Configuration", Job{})

	if strings.Contains(out, "Unique identifier for the job") {
		t.Errorf("RenderStruct must skip InternalFields entry Job.ID")
	}
	if !strings.Contains(out, "| Field | Type | JSON key | Description |") {
		t.Errorf("RenderStruct should render the table header line")
	}
	if !strings.Contains(out, "schedule") || !strings.Contains(out, "Cron expression") {
		t.Errorf("RenderStruct should render documented field Job.Schedule with its description")
	}
}
