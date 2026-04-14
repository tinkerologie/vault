package engine

import (
	"fmt"
	"io"
	"os"
)

// copyFile copies a file from src to dst. It reports progress via the optional
// progress callback (may be nil). The callback receives the percentage complete.
func copyFile(src, dst string) error {
	return copyFileWithProgress(src, dst, nil)
}

// copyFileWithProgress copies a file from src to dst, calling onProgress with
// the number of bytes copied so far after each chunk.
func copyFileWithProgress(src, dst string, onProgress func(bytesCopied int64)) error {
	in, err := os.Open(src) // #nosec G304 — src paths come from libvirt domain XML (trusted system data)
	if err != nil {
		return fmt.Errorf("opening source %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source %s: %w", src, err)
	}

	normalizedDst, err := normalizeRestorePath(dst)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(normalizedDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()) // #nosec G304 — normalizedDst validated by normalizeRestorePath
	if err != nil {
		return fmt.Errorf("creating dest %s: %w", normalizedDst, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	buf := make([]byte, 1024*1024) // 1 MiB buffer
	var copied int64
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("writing to %s: %w", normalizedDst, writeErr)
			}
			copied += int64(n)
			if onProgress != nil {
				onProgress(copied)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading from %s: %w", src, readErr)
		}
	}

	return nil
}
