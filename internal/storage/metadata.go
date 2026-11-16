package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var errBoundedMetadataUnavailable = errors.New("bounded storage metadata unavailable")

// metadataAdapter bounds review-time listings and sidecar reads. The Adapter
// interface has no per-call context, so the probe's context is held here
// (an exception to the no-context-in-structs rule); the adapter lives only
// for one probe and is closed when that probe ends.
type metadataAdapter struct {
	Adapter
	ctx        context.Context
	limit      int
	splitSizes bool
}

func newMetadataAdapter(provider Adapter, opts Options) *metadataAdapter {
	ctx := opts.MetadataProbeContext
	if ctx == nil {
		ctx = context.Background()
	}
	switch p := provider.(type) {
	case *WebDAVAdapter:
		p.requestContext = ctx
	case *NFSAdapter:
		// The metadata probe must not mount the export with write access.
		p.config.Options = strings.Trim(strings.Join([]string{p.config.Options, "ro"}, ","), ",")
	}
	return &metadataAdapter{Adapter: provider, ctx: ctx, limit: opts.MetadataProbeLimit, splitSizes: opts.MetadataProbeSplitSizes}
}

func (a *metadataAdapter) Close() error {
	CloseAdapter(a.Adapter)
	return nil
}

func (a *metadataAdapter) List(prefix string) ([]FileInfo, error) {
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	switch provider := a.Adapter.(type) {
	case *LocalAdapter:
		return provider.listBounded(a.ctx, prefix, a.limit)
	case *NFSAdapter:
		if err := provider.ready(); err != nil {
			return nil, err
		}
		return provider.local.listBounded(a.ctx, prefix, a.limit)
	case *S3Adapter:
		return provider.listBounded(a.ctx, prefix, a.limit)
	case *SFTPAdapter:
		return provider.listBounded(a.ctx, prefix, a.limit)
	case *SMBAdapter:
		return provider.listBounded(a.ctx, prefix, a.limit)
	default:
		// WebDAV listings read split-file sidecars; callers fall back to
		// recorded names and single-object Stat.
		return nil, errBoundedMetadataUnavailable
	}
}

func (a *metadataAdapter) Stat(p string) (FileInfo, error) {
	if err := a.ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	switch provider := a.Adapter.(type) {
	case *S3Adapter:
		return provider.statBounded(a.ctx, p)
	case *WebDAVAdapter:
		return provider.statBounded(p, a.splitSizes)
	default:
		return a.Adapter.Stat(p)
	}
}

func (l *LocalAdapter) listBounded(ctx context.Context, prefix string, limit int) ([]FileInfo, error) {
	dir, err := l.fullPath(prefix, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errBoundedMetadataUnavailable
	}
	handle, err := os.Open(dir) // #nosec G304 -- fullPath checks containment.
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	entries, err := handle.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errBoundedMetadataUnavailable
	}
	out := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, errBoundedMetadataUnavailable
		}
		out = append(out, FileInfo{Path: filepath.Join(prefix, entry.Name()), Size: info.Size(), IsDir: info.IsDir()})
	}
	return out, nil
}

// boundedEntries converts one directory read into a complete listing, or
// fails closed when it is over the limit or holds anything other than
// regular files and directories (links are not artifact listings).
func boundedEntries(ctx context.Context, prefix string, limit int, entries []os.FileInfo, join func(string, string) string) ([]FileInfo, error) {
	if len(entries) > limit {
		return nil, errBoundedMetadataUnavailable
	}
	out := make([]FileInfo, 0, len(entries))
	for _, info := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, errBoundedMetadataUnavailable
		}
		out = append(out, FileInfo{Path: join(prefix, info.Name()), Size: info.Size(), ModTime: info.ModTime(), IsDir: info.IsDir()})
	}
	return out, nil
}

func (s *SFTPAdapter) listBounded(ctx context.Context, prefix string, limit int) (_ []FileInfo, retErr error) {
	conn, err := s.pool.get()
	if err != nil {
		return nil, err
	}
	defer func() { s.pool.put(conn, retErr) }()
	client := conn.(*sftpConnection).sftp
	fullPath, err := s.fullPath(prefix, false)
	if err != nil {
		return nil, err
	}
	if err := verifyRemoteNoSymlinkEscape(client, s.config.BasePath, fullPath); err != nil {
		return nil, err
	}
	// pkg/sftp v1.13.12 has no count-bounded directory read: ReadDirContext
	// is the only public READDIR path and reads the whole directory. It
	// checks ctx between READDIR packets and, when cancelled, returns the
	// entries read so far, so the bound here is time (the probe deadline)
	// plus the limit applied after the read.
	entries, err := client.ReadDirContext(ctx, fullPath)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, wrapSFTPListError(err, fullPath)
	}
	return boundedEntries(ctx, prefix, limit, entries, remoteJoin)
}

// smbContextShare is implemented by the live go-smb2 share so a probe's
// directory read stops when the probe context ends.
type smbContextShare interface {
	withContext(context.Context) smbShare
}

func (s smb2Share) withContext(ctx context.Context) smbShare {
	return smb2Share{s.Share.WithContext(ctx)}
}

func (s *SMBAdapter) listBounded(ctx context.Context, prefix string, limit int) ([]FileInfo, error) {
	fullPath, err := s.fullPath(prefix, false)
	if err != nil {
		return nil, err
	}
	share, session, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer session.Logoff()
	defer share.Umount()
	reader := share
	if scoped, ok := share.(smbContextShare); ok {
		reader = scoped.withContext(ctx)
	}
	entries, err := readSMBDirBounded(reader, fullPath, limit+1)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, wrapSMBListError(err, fullPath)
	}
	return boundedEntries(ctx, prefix, limit, entries, func(dir, name string) string { return filepath.Join(dir, name) })
}

// readSMBDirBounded reads at most max entries of a directory, so a listing
// over the limit is refused without enumerating the rest of it.
func readSMBDirBounded(share smbShare, dir string, max int) (_ []os.FileInfo, retErr error) {
	f, err := share.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := f.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	var entries []os.FileInfo
	for len(entries) < max {
		batch, err := f.Readdir(max - len(entries))
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
	}
	return entries, nil
}

func (a *S3Adapter) listBounded(ctx context.Context, prefix string, limit int) ([]FileInfo, error) {
	key, err := a.keyFor(prefix, false)
	if err != nil {
		return nil, err
	}
	page, err := a.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(a.config.Bucket), Prefix: aws.String(key + "/"),
		Delimiter: aws.String("/"), MaxKeys: aws.Int32(int32(limit)),
	}, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return nil, err
	}
	if aws.ToBool(page.IsTruncated) || len(page.Contents)+len(page.CommonPrefixes) > limit {
		return nil, errBoundedMetadataUnavailable
	}
	out := make([]FileInfo, 0, len(page.Contents))
	for _, object := range page.Contents {
		name := strings.TrimPrefix(aws.ToString(object.Key), key+"/")
		if name != "" && !strings.Contains(name, "/") {
			out = append(out, FileInfo{Path: filepath.Join(prefix, name), Size: aws.ToInt64(object.Size)})
		}
	}
	return out, nil
}

func (a *S3Adapter) statBounded(ctx context.Context, p string) (FileInfo, error) {
	key, err := a.keyFor(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	result, err := a.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(a.config.Bucket), Key: aws.String(key),
	}, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
	var missing *types.NotFound
	if errors.As(err, &missing) {
		return FileInfo{}, fs.ErrNotExist
	}
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: p, Size: aws.ToInt64(result.ContentLength)}, nil
}

// WebDAV's normal Stat opens a split-file sidecar. Probes inspect metadata
// only, so the logical size of a split file remains unknown unless the probe
// allows one bounded manifest read.
func (w *WebDAVAdapter) statBounded(p string, splitSizes bool) (FileInfo, error) {
	full, err := w.fullPath(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	c := w.client()
	info, err := c.Stat(full)
	if isWebDAVNotFound(err) {
		_, sidecarErr := c.Stat(webDAVManifestPath(full))
		if isWebDAVNotFound(sidecarErr) {
			return FileInfo{}, fs.ErrNotExist
		}
		if sidecarErr != nil || !splitSizes {
			return FileInfo{}, errBoundedMetadataUnavailable
		}
		manifest, found, err := w.readManifest(c, full)
		if err != nil || !found {
			return FileInfo{}, errBoundedMetadataUnavailable
		}
		return FileInfo{Path: p, Size: manifest.Size}, nil
	}
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: p, Size: info.Size(), IsDir: info.IsDir()}, nil
}
