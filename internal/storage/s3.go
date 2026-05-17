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
	// UploadTimeoutMinutes caps how long a single object upload (including
	// multipart transfers) may take. Defaults to 240 (4 hours) when 0 or
	// unset, matching the runner's job-level timeout. Negative values are
	// rejected. Metadata operations (List/Stat/Delete/TestConnection/Read)
	// continue to use a short 5-minute timeout.
	UploadTimeoutMinutes int `json:"upload_timeout_minutes"`
	// PartSizeMB controls the multipart upload part size (in MiB) passed to
	// the AWS transfermanager. The S3 protocol caps the number of parts per
	// multipart upload at 10,000, so the part size directly bounds the
	// maximum object size: ceiling = PartSizeMB * 10000.
	//
	// transfermanager defaults to 8 MiB → 80 GB ceiling, which is too low
	// for whole-folder backups (e.g. Immich at 281 GB busts the limit with
	// "exceeded total allowed S3 limit MaxUploadParts (10000)"). It also
	// only auto-scales when the input stream's size is known up-front, which
	// is never the case for our age-encrypted streams (io.PipeReader has no
	// Size()/ContentLength).
	//
	// Vault therefore defaults to 64 MiB → 640 GB ceiling, large enough for
	// every home-server workload we've seen. Power users can raise it for
	// multi-TB datasets (256 MiB → 2.5 TB, 1024 MiB → 10 TB). Note that
	// per-upload peak memory ≈ PartSizeMB × concurrency (default 5), so
	// 1 GiB parts cost ~5 GiB of RAM during the upload.
	//
	// Valid range: 5–5120 (S3/B2 protocol minimum and maximum). 0 = default.
	PartSizeMB int `json:"part_size_mb,omitempty"`
}

// defaultS3UploadTimeout is the default per-upload deadline applied when
// UploadTimeoutMinutes is 0 or unset. It matches the runner's job-level
// timeout so that a single large multipart upload is not cut short.
const defaultS3UploadTimeout = 240 * time.Minute

// S3 protocol bounds for the multipart PartSize header. AWS S3 and every
// S3-compatible service we test against (Backblaze B2, MinIO, Cloudflare R2,
// Wasabi) reject parts outside this range; the AWS SDK's transfermanager
// docs the same minimum (5 MiB).
const (
	minS3PartSizeMB     = 5
	maxS3PartSizeMB     = 5120 // 5 GiB upper bound
	defaultS3PartSizeMB = 64
)

// S3Adapter implements Adapter against an S3 bucket. Unlike SFTP/SMB, the
// underlying client is HTTP-based and pools connections internally, so we
// build it once in the constructor and reuse it for the adapter's lifetime.
type S3Adapter struct {
	config           S3Config
	client           *s3.Client
	uploader         *transfermanager.Client
	uploadTimeout    time.Duration
	partSizeBytes    int64
	requestChecksum  aws.RequestChecksumCalculation
	responseChecksum aws.ResponseChecksumValidation
}

// NewS3Adapter validates the config and constructs an S3 client.
//
// Validation is intentionally permissive: AccessKey and SecretKey are not
// required because some deployments rely on instance/IRSA credentials provided
// by the Go SDK's default chain (AWS_*, EC2 metadata, etc.). When both are
// blank the adapter will fall back to the SDK default. Supplying only one of
// the two is rejected as a configuration error to avoid silently falling back
// to the default chain when the operator clearly intended static credentials.
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
	if cfg.UploadTimeoutMinutes < 0 {
		return nil, fmt.Errorf("s3: upload_timeout_minutes must be >= 0, got %d", cfg.UploadTimeoutMinutes)
	}
	uploadTimeout := defaultS3UploadTimeout
	if cfg.UploadTimeoutMinutes > 0 {
		uploadTimeout = time.Duration(cfg.UploadTimeoutMinutes) * time.Minute
	}
	partSizeMB := defaultS3PartSizeMB
	if cfg.PartSizeMB != 0 {
		if cfg.PartSizeMB < minS3PartSizeMB {
			return nil, fmt.Errorf("s3: part_size_mb must be >= %d (S3 minimum), got %d", minS3PartSizeMB, cfg.PartSizeMB)
		}
		if cfg.PartSizeMB > maxS3PartSizeMB {
			return nil, fmt.Errorf("s3: part_size_mb must be <= %d (S3 maximum 5 GiB), got %d", maxS3PartSizeMB, cfg.PartSizeMB)
		}
		partSizeMB = cfg.PartSizeMB
	}
	partSizeBytes := int64(partSizeMB) * 1024 * 1024

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	// When the user configures a custom Endpoint they are talking to an
	// S3-compatible service (MEGA, Backblaze B2, IDrive E2, MinIO older
	// builds, …), not real AWS. Since aws-sdk-go-v2 v1.32 the S3 client
	// auto-injects a flexible-checksum trailer (x-amz-trailer:
	// x-amz-checksum-crc32) on every PutObject / UploadPart and includes it
	// in the SigV4 canonical request. AWS S3 honours the trailer; most
	// S3-compatible gateways do not, recompute the signature without it,
	// and respond `403 SignatureDoesNotMatch` — visible to users as
	// "Test Connection passes, every upload fails" because HeadBucket has
	// no body and therefore no trailer. Closes #88 follow-up (MEGA) and
	// matches the no-trailer behaviour of Kopia's minio-go based S3
	// implementation, which is why Kopia works against the same gateways
	// out of the box. Vault already verifies object integrity end-to-end
	// via SHA-256 in the runner (see uploadStagedFiles), so the trailer
	// adds no real safety here.
	if cfg.Endpoint != "" {
		loadOpts = append(loadOpts,
			awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
			awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
		)
	}
	haveAccess := cfg.AccessKey != ""
	haveSecret := cfg.SecretKey != ""
	switch {
	case haveAccess && haveSecret:
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	case haveAccess != haveSecret:
		return nil, fmt.Errorf("s3: partial credentials provided; access_key and secret_key must both be set or both be empty")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	clientOpts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, s3.WithEndpointResolverV2(&staticS3EndpointResolver{
			endpoint:     cfg.Endpoint,
			usePathStyle: cfg.ForcePathStyle,
		}))
	}
	if cfg.ForcePathStyle {
		clientOpts = append(clientOpts, func(o *s3.Options) { o.UsePathStyle = true })
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)
	// Bind the configured PartSize to the uploader so multipart transfers
	// stay under the 10,000-part ceiling even for streams whose size is
	// unknown up-front (age-encrypted PipeReader has no Size/ContentLength,
	// which prevents transfermanager's built-in auto-scale at upload time).
	// Closes the MaxUploadParts failure mode on #95.
	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = partSizeBytes
	})
	return &S3Adapter{
		config:           cfg,
		client:           client,
		uploader:         uploader,
		uploadTimeout:    uploadTimeout,
		partSizeBytes:    partSizeBytes,
		requestChecksum:  awsCfg.RequestChecksumCalculation,
		responseChecksum: awsCfg.ResponseChecksumValidation,
	}, nil
}

// staticS3EndpointResolver routes every S3 request to a fixed endpoint,
// preserving the bucket and signing region resolved by the SDK. This is the
// SDK v2 idiomatic replacement for the deprecated single-endpoint flag.
//
// When the SDK is configured with UsePathStyle (the default for our
// custom-endpoint case), the bucket name normally appears as the first path
// segment. With a custom EndpointResolverV2 the SDK does *not* auto-inject
// the bucket — it expects the resolver to return an endpoint whose path
// already contains the bucket. We therefore explicitly join the bucket from
// EndpointParameters into the path. Without this, requests are sent to
// "/" or "/<key>" instead of "/<bucket>/<key>", causing MinIO/AWS to
// respond with NoSuchBucket or BadRequest.
type staticS3EndpointResolver struct {
	endpoint     string
	usePathStyle bool
}

func (r *staticS3EndpointResolver) ResolveEndpoint(_ context.Context, params s3.EndpointParameters) (smithyendpoints.Endpoint, error) {
	u, err := neturl.Parse(r.endpoint)
	if err != nil {
		return smithyendpoints.Endpoint{}, fmt.Errorf("s3: parse endpoint %q: %w", r.endpoint, err)
	}
	if params.Bucket != nil && *params.Bucket != "" {
		bucket := *params.Bucket
		if r.usePathStyle {
			// Path-style: prepend bucket to URL path.
			u.Path = path.Join("/", u.Path, bucket)
		} else {
			// Virtual-host style: prepend bucket to host (only safe when the
			// endpoint host is a real DNS name, not an IP literal).
			u.Host = bucket + "." + u.Host
		}
	}
	return smithyendpoints.Endpoint{URI: *u}, nil
}

func ctxOp() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// ctxUpload returns a context with the adapter's configured upload timeout.
// Used by Write() so that large multipart uploads (which can run for hours)
// are not aborted by the short metadata-operation timeout.
func (a *S3Adapter) ctxUpload() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), a.uploadTimeout)
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
	ctx, cancel := a.ctxUpload()
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
	// The op context governs the GetObject request *and* the lifetime of the
	// returned body stream — cancelling it before the caller finishes reading
	// would abort the download. Cancel only when the caller closes the body.
	ctx, cancel := ctxOp()
	out, err := a.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	return &cancelOnCloseReader{ReadCloser: out.Body, cancel: cancel}, nil
}

// cancelOnCloseReader pairs an S3 response body with the context cancel func
// for the GetObject request. Closing the reader cancels the context, ensuring
// no goroutine/timer is left dangling once the caller is done reading.
type cancelOnCloseReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReader) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
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

	ctx, cancel := ctxOp()
	defer cancel()
	var (
		out    []FileInfo
		token  *string
		basePf = func() string {
			p := strings.Trim(a.config.BasePath, "/")
			if p == "" {
				return ""
			}
			return p + "/"
		}()
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
