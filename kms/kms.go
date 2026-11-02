// Package kms wraps small secrets under a Cloud KMS key, over its REST API, with
// the workload's token from the metadata server. It exists so that a key at rest
// can be opened only with the cloud's permission, and every opening is in the KMS
// audit log. It is the two calls that needs, and no client library.
package kms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// KeyEnv names the variable that holds the key's resource name. Set, the
	// secrets it wraps are the only copy: there is no fallback to anything else.
	KeyEnv = "MISHMAR_KMS_KEY"
	// EndpointEnv overrides the API's address: a private endpoint, or a test.
	EndpointEnv = "MISHMAR_KMS_ENDPOINT"
	// MetadataHostEnv is the variable Google's own libraries read for the
	// metadata server's address.
	MetadataHostEnv = "GCE_METADATA_HOST"

	defaultEndpoint     = "https://cloudkms.googleapis.com"
	defaultMetadataHost = "metadata.google.internal"
	tokenPath           = "/computeMetadata/v1/instance/service-accounts/default/token"
	callTimeout         = 10 * time.Second
	// tokenTries and tokenRetryWait cover a new pod: GKE's metadata server takes a
	// few seconds to answer it, and Google's advice is to try again.
	tokenTries     = 8
	tokenRetryWait = time.Second
)

var keyName = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`)

// Client wraps and unwraps under one key.
type Client struct {
	key      string
	endpoint string
	metadata string
	http     *http.Client
	retry    time.Duration
}

// New makes a client for the key's resource name:
// projects/P/locations/L/keyRings/R/cryptoKeys/K.
func New(key string) (*Client, error) {
	if !keyName.MatchString(key) {
		return nil, fmt.Errorf("kms: %q is not a key's resource name (projects/P/locations/L/keyRings/R/cryptoKeys/K)", key)
	}
	endpoint := strings.TrimRight(os.Getenv(EndpointEnv), "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	metadata := os.Getenv(MetadataHostEnv)
	if metadata == "" {
		metadata = defaultMetadataHost
	}
	// Proxy nil on purpose: a cell's HTTPS_PROXY is the way out to the venues, and
	// neither this API nor the metadata server is to go through it.
	return &Client{
		key: key, endpoint: endpoint, metadata: metadata, retry: tokenRetryWait,
		http: &http.Client{Timeout: callTimeout, Transport: &http.Transport{Proxy: nil}},
	}, nil
}

// Key is the key's resource name.
func (c *Client) Key() string { return c.key }

// Error is an answer from KMS that was not a success.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("kms: %d: %s", e.Status, e.Message) }

func crc(b []byte) string {
	return strconv.FormatUint(uint64(crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))), 10)
}

// Encrypt wraps plaintext. The same aad must be given to Decrypt.
func (c *Client) Encrypt(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	var out struct {
		Name             string `json:"name"`
		Ciphertext       []byte `json:"ciphertext"`
		CiphertextCrc32c string `json:"ciphertextCrc32c"`
		VerifiedPlain    bool   `json:"verifiedPlaintextCrc32c"`
		VerifiedAAD      bool   `json:"verifiedAdditionalAuthenticatedDataCrc32c"`
	}
	err := c.call(ctx, "encrypt", map[string]any{
		"plaintext":                         plaintext,
		"plaintextCrc32c":                   crc(plaintext),
		"additionalAuthenticatedData":       aad,
		"additionalAuthenticatedDataCrc32c": crc(aad),
	}, &out)
	if err != nil {
		return nil, err
	}
	if !out.VerifiedPlain || !out.VerifiedAAD || out.CiphertextCrc32c != crc(out.Ciphertext) {
		return nil, errors.New("kms: encrypt: the answer failed its integrity checks")
	}
	if !strings.HasPrefix(out.Name, c.key+"/") {
		return nil, errors.New("kms: encrypt: the answer names another key")
	}
	return out.Ciphertext, nil
}

// Decrypt unwraps what Encrypt returned, under the same aad.
func (c *Client) Decrypt(ctx context.Context, ciphertext, aad []byte) ([]byte, error) {
	var out struct {
		Plaintext []byte `json:"plaintext"`
		Crc32c    string `json:"plaintextCrc32c"`
	}
	err := c.call(ctx, "decrypt", map[string]any{
		"ciphertext":                        ciphertext,
		"ciphertextCrc32c":                  crc(ciphertext),
		"additionalAuthenticatedData":       aad,
		"additionalAuthenticatedDataCrc32c": crc(aad),
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Crc32c != crc(out.Plaintext) {
		return nil, errors.New("kms: decrypt: the answer failed its integrity check")
	}
	return out.Plaintext, nil
}

func (c *Client) call(ctx context.Context, verb string, in, out any) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/"+c.key+":"+verb, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kms: %s: %w", verb, redact(err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("kms: %s: %w", verb, err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &Error{Status: res.StatusCode, Message: e.Error.Message}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("kms: %s: the answer is not valid", verb)
	}
	return nil
}

// token asks the metadata server for the workload's access token.
func (c *Client) token(ctx context.Context) (string, error) {
	return metadataToken(ctx, c.metadata, c.http, c.retry)
}

// Token asks the metadata server for the workload's access token, as the
// client does for KMS: for the other Google APIs a workload speaks to with the
// same identity, such as its bucket. The server's address is MetadataHostEnv or
// the default.
func Token(ctx context.Context) (string, error) {
	host := os.Getenv(MetadataHostEnv)
	if host == "" {
		host = defaultMetadataHost
	}
	return metadataToken(ctx, host, &http.Client{Timeout: callTimeout, Transport: &http.Transport{Proxy: nil}}, tokenRetryWait)
}

// metadataToken tries for a while: a new pod's metadata server takes a few
// seconds to answer, and Google's advice is to try again. Nothing here is called
// often enough for a token to be worth keeping.
func metadataToken(ctx context.Context, host string, client *http.Client, retry time.Duration) (string, error) {
	var last error
	for try := 0; try < tokenTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retry):
			}
		}
		token, err := tokenOnce(ctx, host, client)
		if err == nil {
			return token, nil
		}
		last = err
	}
	return "", fmt.Errorf("kms: no token from the metadata server: %w", last)
}

func tokenOnce(ctx context.Context, host string, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+tokenPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	res, err := client.Do(req)
	if err != nil {
		return "", redact(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", res.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&out); err != nil || out.AccessToken == "" {
		return "", errors.New("the answer holds no token")
	}
	return out.AccessToken, nil
}

// redact keeps the reason for a failed call and drops its URL.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Decode reads the base64 form a wrapped secret is kept in.
func Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(strings.TrimSpace(s)) }

// Encode is the base64 form a wrapped secret is kept in.
func Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
