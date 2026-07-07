package engine

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// precompressedExtensions lists file types whose payload is already
// compressed at the format level. Re-compressing them with gzip/zstd
// burns CPU and rarely shaves more than 1-2% off the size. Used by
// MaybeDowngradeCompression to auto-disable the outer compressor when
// a source folder is predominantly media / archives.
var precompressedExtensions = map[string]struct{}{
	// generic compressed archives
	".gz": {}, ".zst": {}, ".xz": {}, ".bz2": {}, ".lz4": {}, ".zip": {}, ".7z": {}, ".rar": {},
	// video
	".mp4": {}, ".mkv": {}, ".webm": {}, ".mov": {}, ".avi": {}, ".m4v": {}, ".ts": {},
	// audio
	".mp3": {}, ".aac": {}, ".opus": {}, ".ogg": {}, ".flac": {}, ".m4a": {},
	// images
	".jpg": {}, ".jpeg": {}, ".png": {}, ".webp": {}, ".heic": {}, ".heif": {}, ".gif": {},
	// docs / containers
	".pdf": {}, ".epub": {}, ".mobi": {}, ".docx": {}, ".xlsx": {}, ".pptx": {}, ".odt": {},
	// Vault's own already-encrypted artifacts (would appear in nested backups)
	".age": {},
}

// downgradeThresholdPercent is the precompressed-byte ratio at which we
// stop spending CPU on the outer compressor. Tuned conservatively: at 70%
// the savings from compressing the remaining 30% are usually swamped by
// the tar overhead anyway, so dropping to no-op compression keeps the same
// archive size within a single-digit margin while halving wall-clock time
// on media-heavy folders.
const downgradeThresholdPercent = 70

// MaybeDowngradeCompression scans srcDir and, if the share of bytes in
// already-compressed file formats exceeds downgradeThresholdPercent and the
// caller asked for an actual compressor, returns CompressionNone and logs
// the decision. Otherwise it returns the requested compression unchanged.
//
// Errors during the scan (permission, broken symlink) are non-fatal: any
// path we can't stat is excluded from the totals and the scan continues.
// If the directory ends up with 0 total bytes (empty source) we leave the
// requested compression in place — nothing to optimise.
//
// Designed to be called by folder / plugin / container backups before
// tarDirectory. Single-pass over the source tree; for huge trees (millions
// of files) the walk dominates I/O cost, but that's the same walk
// tarDirectory is about to do anyway — there's no double-read.
func MaybeDowngradeCompression(srcDir, requested string) string {
	algo, _ := splitCompression(requested)
	switch algo {
	case CompressionGzip, CompressionZstd:
		// fall through
	default:
		// "none" or unknown — nothing to downgrade
		return requested
	}

	var totalBytes, precompressedBytes int64
	walkErr := filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		size := info.Size()
		totalBytes += size
		if isPrecompressedExt(p) {
			precompressedBytes += size
		}
		return nil
	})
	if walkErr != nil {
		// Walk-level error (e.g. root unreadable) — leave compression alone.
		log.Printf("engine: compression auto-downgrade walk failed for %s: %v (keeping %s)", srcDir, walkErr, requested)
		return requested
	}
	if totalBytes == 0 {
		return requested
	}

	pct := int((precompressedBytes * 100) / totalBytes)
	if pct >= downgradeThresholdPercent {
		log.Printf("engine: auto-downgrading compression for %s from %s to none (%d%% of %d bytes is already compressed)",
			srcDir, requested, pct, totalBytes)
		return CompressionNone
	}
	return requested
}

// isPrecompressedExt reports whether a file path has an extension we
// recognise as already-compressed. Case-insensitive on the extension.
func isPrecompressedExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := precompressedExtensions[ext]
	return ok
}
