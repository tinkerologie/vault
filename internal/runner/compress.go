package runner

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/ruaan-deysel/vault/internal/crypto"
)

// gzipMagic and zstdMagic are the leading bytes of each codec's container
// format. We use them to decide whether a downloaded file actually needs to be
// transport-decompressed during restore.
var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// looksCompressed reports whether head (the first bytes of a stored object)
// begins with a gzip or zstd container magic number. It is the single source
// of truth for "is this object transport-compressed", shared by the restore
// decompression step and the parallel-download eligibility predicate so the
// two can never diverge.
func looksCompressed(head []byte) bool {
	return (len(head) >= 2 && bytes.Equal(head[:2], gzipMagic)) ||
		(len(head) >= 4 && bytes.Equal(head[:4], zstdMagic))
}

// OpenStoredStream undoes the storage wrapping of one uploaded object: it
// decrypts a ".age" object with passphrase and then strips transport
// compression detected from the content, the same pipeline a classic restore
// download uses. It returns the plain stream, a close func, and the object's
// local name with the ".age"/".gz"/".zst" suffixes removed. A ".age" object
// with an empty passphrase is an error rather than ciphertext passed through.
func OpenStoredStream(r io.Reader, name, passphrase string) (io.Reader, func() error, string, error) {
	src := r
	closeDecrypt := func() error { return nil }
	if base, ok := strings.CutSuffix(name, ".age"); ok {
		if passphrase == "" {
			return nil, nil, "", fmt.Errorf("%s is encrypted: a backup passphrase is required", name)
		}
		dec, err := crypto.DecryptReader(passphrase, r)
		if err != nil {
			return nil, nil, "", fmt.Errorf("decrypting %s: %w", name, err)
		}
		src, closeDecrypt, name = dec, dec.Close, base
	}
	plain, closeDecompress, local, err := decompressStoredReader(src, name, "")
	if err != nil {
		_ = closeDecrypt()
		return nil, nil, "", err
	}
	return plain, func() error {
		err := closeDecompress()
		if cerr := closeDecrypt(); err == nil {
			err = cerr
		}
		return err
	}, local, nil
}

// decompressStoredReader unwraps one layer of transport compression from a
// restored file. The engine is the single source of truth for archive-level
// compression since 2026.05.03, so newly produced backups never get a
// transport wrap and must be left untouched here (otherwise plain files such
// as config.json fail with "magic number mismatch"). For legacy backups
// produced before that change — where the runner double-wrapped every upload
// with the job's configured codec — this function still strips the outer
// layer when the bytes really are compressed.
//
// Decisions are made by peeking the first four bytes against gzip/zstd magic
// numbers, never by trusting the filename extension or the job's compression
// setting. The corresponding extension is stripped from the returned name
// only if a layer was actually peeled.
func decompressStoredReader(r io.Reader, fileName, compression string) (io.Reader, func() error, string, error) {
	_ = compression // retained for API stability; detection is content-based.

	br := bufio.NewReaderSize(r, 4096)
	peek, err := br.Peek(4)
	if err != nil && err != io.EOF {
		return nil, nil, "", fmt.Errorf("peeking %s: %w", fileName, err)
	}

	// Gate the compressed-vs-plain decision on looksCompressed so the
	// decompression decision and the parallel-download eligibility predicate can
	// never diverge. Codec discrimination (gzip vs zstd) stays here.
	if !looksCompressed(peek) {
		return br, func() error { return nil }, fileName, nil
	}

	if len(peek) >= 2 && bytes.Equal(peek[:2], gzipMagic) {
		gr, gerr := gzip.NewReader(br)
		if gerr != nil {
			return nil, nil, "", fmt.Errorf("creating gzip reader: %w", gerr)
		}
		return gr, gr.Close, strings.TrimSuffix(fileName, ".gz"), nil
	}
	// zstd — the only other codec looksCompressed recognises.
	zr, zerr := zstd.NewReader(br)
	if zerr != nil {
		return nil, nil, "", fmt.Errorf("creating zstd reader: %w", zerr)
	}
	return zr, func() error { zr.Close(); return nil }, strings.TrimSuffix(fileName, ".zst"), nil
}

// transportCompressionSuffix maps a job compression setting to the filename
// suffix its transport wrap carries. Empty for "none" or unknown codecs.
func transportCompressionSuffix(compression string) string {
	switch compression {
	case "gzip":
		return ".gz"
	case "zstd":
		return ".zst"
	default:
		return ""
	}
}

// transportCompressReader wraps src with the job's configured codec. Used for
// item types whose engine stages uncompressed artifacts (VM disk images and
// sidecars) — container/folder/plugin archives are compressed by the engine
// and must not be wrapped again. Closing the returned reader stops the
// background compressor; the caller still owns closing src.
func transportCompressReader(compression string, src io.Reader) (io.ReadCloser, error) {
	pr, pw := io.Pipe()

	var cw io.WriteCloser
	switch compression {
	case "gzip":
		cw = gzip.NewWriter(pw)
	case "zstd":
		zw, err := zstd.NewWriter(pw)
		if err != nil {
			_ = pw.Close()
			_ = pr.Close()
			return nil, fmt.Errorf("creating zstd writer: %w", err)
		}
		cw = zw
	default:
		_ = pw.Close()
		_ = pr.Close()
		return nil, fmt.Errorf("unsupported transport compression %q", compression)
	}

	go func() {
		// A compressor panic must surface as a read error on the pipe, not
		// kill the daemon (issue #239).
		defer func() {
			if rec := recover(); rec != nil {
				_ = pw.CloseWithError(fmt.Errorf("compressor panicked: %v", rec))
			}
		}()
		_, copyErr := io.Copy(cw, src)
		if closeErr := cw.Close(); copyErr == nil {
			copyErr = closeErr
		}
		// Propagate the compressor's fate to the reading side; nil means EOF.
		_ = pw.CloseWithError(copyErr)
	}()

	return pr, nil
}
