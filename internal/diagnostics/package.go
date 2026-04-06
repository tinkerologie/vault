package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// PackageAsZip creates a ZIP archive containing the diagnostic bundle as JSON.
func PackageAsZip(bundle *DiagnosticBundle) (io.Reader, error) {
	jsonData, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling diagnostics: %w", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	header := &zip.FileHeader{
		Name:     "diagnostics.json",
		Method:   zip.Deflate,
		Modified: time.Now().UTC(),
	}

	f, err := w.CreateHeader(header)
	if err != nil {
		return nil, fmt.Errorf("creating zip entry: %w", err)
	}

	if _, err := f.Write(jsonData); err != nil {
		return nil, fmt.Errorf("writing zip entry: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing zip writer: %w", err)
	}

	return &buf, nil
}
