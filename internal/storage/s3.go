package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
)

// S3Config holds configuration for an S3 (or S3-compatible) storage adapter.
//
// Endpoint is optional and only required for S3-compatible services such as
// Backblaze B2, MinIO, Cloudflare R2, or Wasabi. Leave empty to use the
// canonical AWS S3 endpoint for the configured Region.
//
// ForcePathStyle should be enabled for endpoints that don't support virtual
// hosted-style addressing (e.g. older MinIO deployments). AWS S3 itself
// supports both styles; modern providers default to virtual hosted-style.
type S3Config struct {
	Bucket         string `json:"bucket"`
	Region         string `json:"region"`
	AccessKey      string `json:"access_key"`
	SecretKey      string `json:"secret_key"`
	Endpoint       string `json:"endpoint"`         // Optional, e.g. "https://s3.us-west-002.backblazeb2.com"
	BasePath       string `json:"base_path"`        // Optional key prefix for all objects.
	ForcePathStyle bool   `json:"force_path_style"` // Optional, for older S3-compatible services.
}

// S3Adapter implements Adapter against an S3 bucket. Unlike SFTP/SMB, the
// underlying client is HTTP-based and pools connections internally, so we
// build it once in the constructor and reuse it for the adapter's lifetime.
type S3Adapter struct {
	config   S3Config
	client   *s3.Client
	uploader *transfermanager.Client
}

// NewS3Adapter validates the config and constructs an S3 client.
//
// Validation is intentionally permissive: AccessKey and SecretKey are not
// required because some deployments rely on instance/IRSA credentials provided
// by the Go SDK's default chain (AWS_*, EC2 metadata, etc.). When both are
// blank the adapter will fall back to the SDK default.
func NewS3Adapter(cfg S3Config) (*S3Adapter, error) {
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.BasePath = strings.Trim(cfg.BasePath, "/")

	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("s3: region is required")
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	clientOpts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, s3.WithEndpointResolverV2(&staticS3EndpointResolver{endpoint: cfg.Endpoint}))
	}
	if cfg.ForcePathStyle {
		clientOpts = append(clientOpts, func(o *s3.Options) { o.UsePathStyle = true })
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)
	return &S3Adapter{
		config:   cfg,
		client:   client,
		uploader: transfermanager.New(client),
	}, nil
}

// staticS3EndpointResolver routes every S3 request to a fixed endpoint,
// preserving the bucket and signing region resolved by the SDK. This is the
// SDK v2 idiomatic replacement for the deprecated single-endpoint flag.
type staticS3EndpointResolver struct {
	endpoint string
}

func (r *staticS3EndpointResolver) ResolveEndpoint(_ context.Context, _ s3.EndpointParameters) (smithyendpoints.Endpoint, error) {
	u, err := neturl.Parse(r.endpoint)
	if err != nil {
		return smithyendpoints.Endpoint{}, fmt.Errorf("s3: parse endpoint %q: %w", r.endpoint, err)
	}
	return smithyendpoints.Endpoint{URI: *u}, nil
}

func ctxOp() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// keyFor joins the configured base path and an operation-supplied path using
// "/" as the separator (S3 keys are virtual). safepath is used to reject
// traversal attempts ("../") regardless of host OS.
func (a *S3Adapter) keyFor(p string, allowRoot bool) (string, error) {
	clean := strings.TrimSpace(p)
	if clean == "" && !allowRoot {
		return "", fmt.Errorf("s3: path is required")
	}
	// Normalise to forward slashes and reject traversal explicitly. We don't
	// use safepath here because it is filepath-based; for S3 keys we just
	// need to forbid ".." segments.
	clean = strings.ReplaceAll(clean, "\\", "/")
	clean = strings.Trim(clean, "/")
	if clean == "" {
		if !allowRoot {
			return "", fmt.Errorf("s3: path is required")
		}
		return strings.Trim(a.config.BasePath, "/"), nil
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("s3: invalid path segment %q", seg)
		}
	}
	if a.config.BasePath != "" {
		clean = path.Join(a.config.BasePath, clean)
	}
	return clean, nil
}

func (a *S3Adapter) Write(p string, reader io.Reader) error {
	key, err := a.keyFor(p, false)
	if err != nil {
		return err
	}
	ctx, cancel := ctxOp()
	defer cancel()
	if _, err := a.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(a.config.Bucket),
		Key:    aws.String(key),
		Body:   reader,
	}); err != nil {
		return fmt.Errorf("s3: upload %s: %w", key, err)
	}
	return nil
}

func (a *S3Adapter) Read(p string) (io.ReadCloser, error) {
	key, err := a.keyFor(p, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := ctxOp()
	defer cancel()
	out, err := a.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	return out.Body, nil
}

func (a *S3Adapter) Delete(p string) error {
	key, err := a.keyFor(p, false)
	if err != nil {
		return err
	}
	ctx, cancel := ctxOp()
	defer cancel()
	if _, err := a.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(a.config.Bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}
	return nil
}

func (a *S3Adapter) List(prefix string) ([]FileInfo, error) {
	key, err := a.keyFor(prefix, true)
	if err != nil {
		return nil, err
	}
	// Ensure the prefix ends in "/" so we don't match same-prefix neighbours.
	if key != "" && !strings.HasSuffix(key, "/") {
		key += "/"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var (
		out    []FileInfo
		token  *string
		basePf = strings.TrimSuffix(strings.Trim(a.config.BasePath, "/")+"/", "/")
	)
	for {
		page, err := a.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(a.config.Bucket),
			Prefix:            aws.String(key),
			ContinuationToken: token,
			Delimiter:         aws.String("/"),
		})
		if err != nil {
			return nil, fmt.Errorf("s3: list %s: %w", key, err)
		}

		for _, cp := range page.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			rel := stripPrefix(*cp.Prefix, basePf)
			out = append(out, FileInfo{
				Path:  strings.TrimSuffix(rel, "/"),
				IsDir: true,
			})
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			rel := stripPrefix(*obj.Key, basePf)
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			var mt time.Time
			if obj.LastModified != nil {
				mt = *obj.LastModified
			}
			out = append(out, FileInfo{
				Path:    rel,
				Size:    size,
				ModTime: mt,
				IsDir:   false,
			})
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

func (a *S3Adapter) Stat(p string) (FileInfo, error) {
	key, err := a.keyFor(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	ctx, cancel := ctxOp()
	defer cancel()
	out, err := a.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(a.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return FileInfo{}, fmt.Errorf("s3: head %s: %w", key, err)
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	var mt time.Time
	if out.LastModified != nil {
		mt = *out.LastModified
	}
	return FileInfo{
		Path:    p,
		Size:    size,
		ModTime: mt,
		IsDir:   false,
	}, nil
}

// TestConnection verifies the bucket exists and the credentials grant access.
// HeadBucket is used because it requires only s3:ListBucket and reports both
// missing buckets and permission failures distinctly.
func (a *S3Adapter) TestConnection() error {
	ctx, cancel := ctxOp()
	defer cancel()
	if _, err := a.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(a.config.Bucket),
	}); err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return fmt.Errorf("s3: bucket %q does not exist or is inaccessible", a.config.Bucket)
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("s3: %s: %s", apiErr.ErrorCode(), apiErr.ErrorMessage())
		}
		return fmt.Errorf("s3: head bucket: %w", err)
	}
	return nil
}

// stripPrefix removes the configured base path from an absolute object key
// so callers see paths relative to the configured destination root.
func stripPrefix(key, basePf string) string {
	if basePf == "" {
		return strings.TrimPrefix(key, "/")
	}
	return strings.TrimPrefix(strings.TrimPrefix(key, basePf), "/")
}

var _ Adapter = (*S3Adapter)(nil)
