package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Config struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
}

type S3Adapter struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3Adapter(config S3Config) (*S3Adapter, error) {
	if config.Region == "" {
		config.Region = "us-east-1"
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = config.Region
			o.Credentials = credentials.NewStaticCredentialsProvider(
				config.AccessKeyID, config.SecretAccessKey, "",
			)
			if config.Endpoint != "" {
				o.BaseEndpoint = aws.String(config.Endpoint)
			}
			if config.ForcePathStyle {
				o.UsePathStyle = true
			}
		},
	}

	client := s3.New(s3.Options{}, opts...)

	return &S3Adapter{
		client: client,
		bucket: config.Bucket,
		prefix: config.Prefix,
	}, nil
}

func (a *S3Adapter) key(path string) string {
	if a.prefix != "" {
		return a.prefix + "/" + path
	}
	return path
}

func (a *S3Adapter) Write(path string, reader io.Reader) error {
	_, err := a.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(a.key(path)),
		Body:   reader,
	})
	return err
}

func (a *S3Adapter) Read(path string) (io.ReadCloser, error) {
	out, err := a.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(a.key(path)),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (a *S3Adapter) Delete(path string) error {
	_, err := a.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(a.key(path)),
	})
	return err
}

func (a *S3Adapter) List(prefix string) ([]FileInfo, error) {
	fullPrefix := a.key(prefix)
	out, err := a.client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket: aws.String(a.bucket),
		Prefix: aws.String(fullPrefix + "/"),
	})
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, obj := range out.Contents {
		files = append(files, FileInfo{
			Path:    *obj.Key,
			Size:    *obj.Size,
			ModTime: *obj.LastModified,
		})
	}
	return files, nil
}

func (a *S3Adapter) Stat(path string) (FileInfo, error) {
	out, err := a.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(a.key(path)),
	})
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    *out.ContentLength,
		ModTime: *out.LastModified,
	}, nil
}

func (a *S3Adapter) TestConnection() error {
	_, err := a.client.HeadBucket(context.Background(), &s3.HeadBucketInput{
		Bucket: aws.String(a.bucket),
	})
	if err != nil {
		return fmt.Errorf("bucket not accessible: %w", err)
	}
	return nil
}

var _ Adapter = (*S3Adapter)(nil)
