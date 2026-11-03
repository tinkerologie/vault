// Package bounded reads data Vault does not control, such as files on backup
// storage and replication peer responses, without letting it use unlimited
// memory. The limits sit far above anything a real backup produces, because a
// limit that refuses a real restore is worse than the problem it prevents.
package bounded

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// ManifestBytes limits a run manifest or similar small JSON file. Manifests
	// hold per-item metadata, not per-file lists, and are normally kilobytes.
	ManifestBytes int64 = 256 << 20
	// IndexBytes limits a tar index sidecar, which has one entry per file at
	// roughly 200 bytes: about 20 million files per archive.
	IndexBytes int64 = 4 << 30
)

// ErrTooLarge means the input exceeded its limit.
var ErrTooLarge = errors.New("input exceeds the size limit")

// ReadAll is io.ReadAll that fails with ErrTooLarge past limit bytes.
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w of %d bytes", ErrTooLarge, limit)
	}
	return data, nil
}

// DecodeJSON decodes one JSON value from r, failing with ErrTooLarge if the
// value runs past limit bytes. It streams, so nothing is buffered beyond what
// decoding the value needs.
func DecodeJSON(r io.Reader, limit int64, v any) error {
	lr := &io.LimitedReader{R: r, N: limit + 1}
	dec := json.NewDecoder(lr)
	err := dec.Decode(v)
	if (err != nil && lr.N <= 0) || dec.InputOffset() > limit {
		return fmt.Errorf("%w of %d bytes", ErrTooLarge, limit)
	}
	return err
}
