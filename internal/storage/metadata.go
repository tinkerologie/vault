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

type metadataAdapter struct {
	Adapter
	ctx   context.Context
	limit int
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
	return &metadataAdapter{Adapter: provider, ctx: ctx, limit: opts.MetadataProbeLimit}
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
	default:
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
		return provider.statBounded(p)
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
// only, so the logical size of a split file remains unknown.
func (w *WebDAVAdapter) statBounded(p string) (FileInfo, error) {
	full, err := w.fullPath(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	c := w.client()
	info, err := c.Stat(full)
	if isWebDAVNotFound(err) {
		if _, sidecarErr := c.Stat(webDAVManifestPath(full)); isWebDAVNotFound(sidecarErr) {
			return FileInfo{}, fs.ErrNotExist
		}
		return FileInfo{}, errBoundedMetadataUnavailable
	}
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: p, Size: info.Size(), IsDir: info.IsDir()}, nil
}
