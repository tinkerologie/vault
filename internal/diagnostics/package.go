package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// PackageAsZip writes the bundle into a ZIP archive split across
// several files so the resulting bundle is human-skim-friendly. The
// top-level diagnostics.json is the bundle struct with the log tail
// removed (the log lives in its own vault.log file). Individual
// sections (jobs, storage, activity, runs, settings, system) are also
// written to their own JSON files for quick targeted inspection.
//
// Splitting matters because the bundle has grown — the previous single
// diagnostics.json was several hundred KB once activity + run logs
// were added. A support engineer reading a ticket benefits from being
// able to `unzip -p bundle.zip jobs.json | jq` without scrolling past
// thousands of activity entries.
func PackageAsZip(bundle *DiagnosticBundle) (io.Reader, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	mtime := bundle.GeneratedAt
	if mtime.IsZero() {
		mtime = time.Now().UTC()
	}

	// Slim copy for the top-level overview — the log tail and the
	// noisier per-section slices are emptied so diagnostics.json stays
	// a one-screen summary. Pointer-share is fine because we restore
	// the original bundle before returning (none of the per-section
	// writes mutate it).
	top := *bundle
	top.LogTail = ""
	top.Activity = nil
	top.Runs = nil
	top.VerifyRuns = nil

	files := []zipFile{
		{name: "diagnostics.json", data: &top},
		{name: "system.json", data: bundle.System},
		{name: "settings.json", data: bundle.Settings},
		{name: "storage.json", data: bundle.Storage},
		{name: "jobs.json", data: bundle.Jobs},
		{name: "runs.json", data: bundle.Runs},
		{name: "activity.json", data: bundle.Activity},
		{name: "replication.json", data: bundle.Replication},
		{name: "runner.json", data: bundle.Runner},
		{name: "entries.json", data: bundle.Entries},
		{name: "verify_runs.json", data: bundle.VerifyRuns},
		{name: "dedup_stats.json", data: bundle.DedupStats},
		{name: "runtime.json", data: bundle.Runtime},
		{name: "pools.json", data: bundle.Pools},
		{name: "connectivity.json", data: bundle.Connectivity},
		{name: "scheduler.json", data: bundle.Scheduler},
	}

	for _, f := range files {
		if err := writeJSON(w, f.name, f.data, mtime); err != nil {
			return nil, err
		}
	}

	// vault.log is a plain text file, already redacted in the bundle.
	if bundle.LogTail != "" {
		if err := writeFile(w, "vault.log", []byte(bundle.LogTail), mtime); err != nil {
			return nil, err
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing zip writer: %w", err)
	}
	return &buf, nil
}

type zipFile struct {
	name string
	data any
}

func writeJSON(w *zip.Writer, name string, data any, mtime time.Time) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", name, err)
	}
	return writeFile(w, name, encoded, mtime)
}

func writeFile(w *zip.Writer, name string, data []byte, mtime time.Time) error {
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: mtime,
	}
	f, err := w.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("creating zip entry %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing zip entry %s: %w", name, err)
	}
	return nil
}
