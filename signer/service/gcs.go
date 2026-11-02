package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/kms"
)

// GCSBackend keeps the sealed entries as objects in a Google Cloud Storage
// bucket, one per key under a prefix: the hosted tier's store. It speaks the
// JSON API with the workload's own token from the metadata server, the same
// identity that opens the master key through KMS, so a cell needs no key of
// any kind to reach its bucket: its Kubernetes identity is granted the
// prefix and nothing else. Put creates with ifGenerationMatch=0, so two
// replicas cannot both create one key. The objects hold ciphertext only; the
// master key never reaches the bucket, and bucket versioning gives every
// change a history.
//
// Any S3-compatible store, GCS's interoperability endpoint with HMAC keys
// included, is S3Backend. This one is for gs:// with no keys at all.
type GCSBackend struct {
	bucket, prefix string
	endpoint       string
	http           *http.Client
	token          func(context.Context) (string, error)
}

// EnvGCSEndpoint overrides the API's address: a test.
const EnvGCSEndpoint = "MISHMAR_GCS_ENDPOINT"

const (
	defaultGCSEndpoint = "https://storage.googleapis.com"
	gcsTimeout         = 30 * time.Second
	gcsMaxObject       = 1 << 20
)

// OpenGCS opens "gs://bucket/prefix". Nothing is read until the store syncs.
func OpenGCS(spec string) (*GCSBackend, error) {
	u, err := url.Parse(spec)
	if err != nil || u.Scheme != "gs" || u.Host == "" {
		return nil, fmt.Errorf("signer service: --store %q is not gs://bucket/prefix", spec)
	}
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	endpoint := strings.TrimRight(os.Getenv(EnvGCSEndpoint), "/")
	if endpoint == "" {
		endpoint = defaultGCSEndpoint
	}
	// Proxy nil on purpose: a cell's HTTPS_PROXY is the engine's way to the venues,
	// and the bucket is reached through Google's private range, not through it.
	return &GCSBackend{
		bucket: u.Host, prefix: prefix, endpoint: endpoint,
		http:  &http.Client{Timeout: gcsTimeout, Transport: &http.Transport{Proxy: nil}},
		token: kms.Token,
	}, nil
}

// Bucket and Prefix are where the entries live.
func (b *GCSBackend) Bucket() string { return b.bucket }
func (b *GCSBackend) Prefix() string { return b.prefix }

func (b *GCSBackend) key(id string) string { return b.prefix + id + ".json" }

func (b *GCSBackend) objectURL(name string) string {
	return b.endpoint + "/storage/v1/b/" + url.PathEscape(b.bucket) + "/o/" + url.PathEscape(name)
}

// do makes one call and returns the status and body; an error is a transport
// failure, not a refusal.
func (b *GCSBackend) do(ctx context.Context, method, rawURL string, body []byte, contentType string) (int, []byte, error) {
	token, err := b.token(ctx)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := b.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("signer service: bucket %s: %w", b.bucket, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, gcsMaxObject))
	if err != nil {
		return 0, nil, fmt.Errorf("signer service: bucket %s: %w", b.bucket, err)
	}
	return res.StatusCode, raw, nil
}

func (b *GCSBackend) refused(what string, status int, raw []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return fmt.Errorf("signer service: bucket %s: %s: status %d: %s", b.bucket, what, status, e.Error.Message)
}

// List names every entry under the prefix, page by page. The prefix is in
// the request, which is what a prefix-conditioned grant requires of a list.
func (b *GCSBackend) List() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gcsTimeout)
	defer cancel()
	var ids []string
	pageToken := ""
	for {
		q := url.Values{"prefix": {b.prefix}, "fields": {"items/name,nextPageToken"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		status, raw, err := b.do(ctx, http.MethodGet, b.endpoint+"/storage/v1/b/"+url.PathEscape(b.bucket)+"/o?"+q.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, b.refused("list", status, raw)
		}
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("signer service: bucket %s: list: %w", b.bucket, err)
		}
		for _, o := range page.Items {
			if id, ok := strings.CutSuffix(strings.TrimPrefix(o.Name, b.prefix), ".json"); ok && id != "" && !strings.Contains(id, "/") {
				ids = append(ids, id)
			}
		}
		if page.NextPageToken == "" {
			return ids, nil
		}
		pageToken = page.NextPageToken
	}
}

func (b *GCSBackend) Get(id string) (Sealed, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gcsTimeout)
	defer cancel()
	status, raw, err := b.do(ctx, http.MethodGet, b.objectURL(b.key(id))+"?alt=media", nil, "")
	if err != nil {
		return Sealed{}, false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return Sealed{}, false, nil
	default:
		return Sealed{}, false, b.refused("get "+id, status, raw)
	}
	var e Sealed
	if err := json.Unmarshal(raw, &e); err != nil {
		return Sealed{}, false, fmt.Errorf("signer service: bucket %s: %s is not a sealed entry", b.bucket, id)
	}
	return e, true, nil
}

// Put creates the entry, and refuses an id already present (ErrHeld).
func (b *GCSBackend) Put(id string, e Sealed) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gcsTimeout)
	defer cancel()
	q := url.Values{"uploadType": {"media"}, "name": {b.key(id)}, "ifGenerationMatch": {"0"}}
	status, raw, err := b.do(ctx, http.MethodPost, b.endpoint+"/upload/storage/v1/b/"+url.PathEscape(b.bucket)+"/o?"+q.Encode(), body, "application/json")
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return ErrHeld
	}
	return b.refused("put "+id, status, raw)
}

func (b *GCSBackend) Delete(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gcsTimeout)
	defer cancel()
	status, raw, err := b.do(ctx, http.MethodDelete, b.objectURL(b.key(id)), nil, "")
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	}
	return b.refused("delete "+id, status, raw)
}

var _ Backend = (*GCSBackend)(nil)
