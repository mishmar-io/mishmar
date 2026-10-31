package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// S3Backend keeps the sealed entries as objects in a bucket, one per key
// under a prefix: the hosted tier's store. Any S3-compatible service does:
// AWS S3, Google Cloud Storage through its interoperability endpoint with
// HMAC keys, Cloudflare R2, MinIO. Put creates with a conditional write
// (If-None-Match: *), so two replicas cannot both create one key. The
// objects hold ciphertext only; the master key never reaches the bucket,
// and bucket versioning gives every change a history.
type S3Backend struct {
	client         *s3.Client
	bucket, prefix string
}

// S3Options configure OpenS3 beyond the bucket and prefix in the spec.
type S3Options struct {
	// Endpoint overrides the service's URL: https://storage.googleapis.com
	// for GCS, a MinIO or R2 URL. Empty is AWS S3.
	Endpoint string
	// Region is the signing region; compatible stores take any, AWS wants
	// the bucket's. Empty takes the SDK's own configuration, else us-east-1.
	Region string
	// AccessKey and SecretKey are static credentials (GCS HMAC keys, a MinIO
	// user); empty takes the SDK's default chain: AWS_* variables, the
	// shared config, an instance or workload identity.
	AccessKey, SecretKey string
	// PathStyle addresses the bucket in the path, as compatible stores do;
	// set by itself whenever Endpoint is.
	PathStyle bool
	// HTTPClient is for tests.
	HTTPClient *http.Client
}

// The variables `signer serve` reads S3Options from; the AWS SDK's own
// (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION, …) work as well.
const (
	EnvS3Endpoint  = "MISHMAR_S3_ENDPOINT"
	EnvS3Region    = "MISHMAR_S3_REGION"
	EnvS3AccessKey = "MISHMAR_S3_ACCESS_KEY"
	EnvS3SecretKey = "MISHMAR_S3_SECRET_KEY"
)

// S3OptionsFromEnv reads the MISHMAR_S3_* variables.
func S3OptionsFromEnv() S3Options {
	return S3Options{
		Endpoint: os.Getenv(EnvS3Endpoint), Region: os.Getenv(EnvS3Region),
		AccessKey: os.Getenv(EnvS3AccessKey), SecretKey: os.Getenv(EnvS3SecretKey),
	}
}

// s3Timeout bounds one call to the store; it is never on an order's path.
const s3Timeout = 30 * time.Second

// OpenS3 opens "s3://bucket/prefix", or "gs://bucket/prefix" for Google
// Cloud Storage through its interoperability endpoint (HMAC keys in the
// access and secret key). Nothing is read until the store syncs.
func OpenS3(ctx context.Context, spec string, o S3Options) (*S3Backend, error) {
	u, err := url.Parse(spec)
	if err != nil || (u.Scheme != "s3" && u.Scheme != "gs") || u.Host == "" {
		return nil, fmt.Errorf("signer service: --store %q is not s3://bucket/prefix or gs://bucket/prefix", spec)
	}
	bucket := u.Host
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	if u.Scheme == "gs" && o.Endpoint == "" {
		o.Endpoint = "https://storage.googleapis.com"
	}
	var loadOpts []func(*awsconfig.LoadOptions) error
	if o.AccessKey != "" || o.SecretKey != "" {
		if o.AccessKey == "" || o.SecretKey == "" {
			return nil, errors.New("signer service: the object store's access key and secret key go together")
		}
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.AccessKey, o.SecretKey, "")))
	}
	if o.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(o.Region))
	}
	if o.HTTPClient != nil {
		loadOpts = append(loadOpts, awsconfig.WithHTTPClient(o.HTTPClient))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("signer service: object store: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	client := s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
		so.UsePathStyle = o.PathStyle || o.Endpoint != ""
		// Compatible stores do not all take the SDK's newer checksum
		// headers; the entries are small and sealed, and a bad read fails to
		// open anyway.
		so.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &S3Backend{client: client, bucket: bucket, prefix: prefix}, nil
}

// Bucket and Prefix are where the entries live.
func (b *S3Backend) Bucket() string { return b.bucket }
func (b *S3Backend) Prefix() string { return b.prefix }

func (b *S3Backend) key(id string) string { return b.prefix + id + ".json" }

func (b *S3Backend) List() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	pages := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{Bucket: aws.String(b.bucket), Prefix: aws.String(b.prefix)})
	var ids []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("object store: list %s/%s: %w", b.bucket, b.prefix, err)
		}
		for _, o := range page.Contents {
			k := aws.ToString(o.Key)
			if !strings.HasPrefix(k, b.prefix) || !strings.HasSuffix(k, ".json") {
				continue
			}
			id := strings.TrimSuffix(strings.TrimPrefix(k, b.prefix), ".json")
			if id == "" || strings.Contains(id, "/") {
				continue
			}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (b *S3Backend) Get(id string) (Sealed, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(b.key(id))})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) || isStatus(err, http.StatusNotFound) {
			return Sealed{}, false, nil
		}
		return Sealed{}, false, fmt.Errorf("object store: get %s: %w", id, err)
	}
	defer out.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(out.Body, 1<<20))
	if err != nil {
		return Sealed{}, false, fmt.Errorf("object store: get %s: %w", id, err)
	}
	var e Sealed
	if err := json.Unmarshal(raw, &e); err != nil {
		return Sealed{}, false, fmt.Errorf("object store: %s is not a sealed entry: %w", b.key(id), err)
	}
	return e, true, nil
}

func (b *S3Backend) Put(id string, e Sealed) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(b.key(id)), Body: bytes.NewReader(raw),
		ContentType: aws.String("application/json"), IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		if isCode(err, "PreconditionFailed") || isStatus(err, http.StatusPreconditionFailed) {
			return fmt.Errorf("%s: %w", id, ErrHeld)
		}
		return fmt.Errorf("object store: put %s: %w", id, err)
	}
	return nil
}

func (b *S3Backend) Delete(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(b.key(id))}); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil
		}
		return fmt.Errorf("object store: delete %s: %w", id, err)
	}
	return nil
}

func isCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

func isStatus(err error, status int) bool {
	var re *smithyhttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == status
}
