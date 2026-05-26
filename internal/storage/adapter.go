package storage

import (
	"context"
	"io"
	"time"
)

type FileInfo struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
}

type Adapter interface {
	Write(path string, reader io.Reader) error
	Read(path string) (io.ReadCloser, error)
	// ReadRange returns a stream limited to `length` bytes starting at
	// `offset`. Used by the dedup layer to fetch small slices of multi-MiB
	// pack files without downloading the whole object.
	//
	// Semantics:
	//   - offset >= file size MUST return an error.
	//   - A range that straddles EOF returns the bytes that exist; the
	//     reader surfaces io.EOF when exhausted (idiomatic io.Reader).
	//   - Closing the returned ReadCloser releases the underlying file
	//     handle / HTTP response body / context cancellation.
	ReadRange(path string, offset, length int64) (io.ReadCloser, error)
	Delete(path string) error
	List(prefix string) ([]FileInfo, error)
	Stat(path string) (FileInfo, error)
	TestConnection() error
	// GetCapacity returns the destination's space accounting. The
	// implementation MUST use the cheapest native API available to the
	// provider (statfs / RFC 4331 PROPFIND / SFTP statvfs / etc.).
	// Adapters whose protocol has no quota concept return a Capacity
	// with TotalBytes == 0; UsedBytes MAY still be populated via a
	// fallback (S3 sums object sizes via ListObjectsV2 pagination).
	//
	// The context honours the caller's deadline so callers can cap the
	// probe (the scheduler uses 60 s; ad-hoc UI refreshes use 30 s).
	GetCapacity(ctx context.Context) (Capacity, error)
}

// rangeReader pairs a length-limited reader with the closer for the
// underlying resource (file handle, HTTP response body, …). Shared by every
// adapter that exposes a `ReadAt`-style primitive so the Close on the
// returned ReadCloser actually releases the source.
type rangeReader struct {
	io.Reader
	closer io.Closer
}

func (r *rangeReader) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

// CloseAdapter closes an adapter if it implements io.Closer.
// Safe to call on any Adapter; adapters without resources are no-ops.
func CloseAdapter(a Adapter) {
	if closer, ok := a.(io.Closer); ok {
		_ = closer.Close()
	}
}
