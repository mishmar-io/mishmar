package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// Client speaks the local API: to the daemon over its socket (Dial), or to
// a Manager in this process (Local).
type Client struct {
	HTTP *http.Client
	base string
}

// Dial is a client of the daemon at the socket.
func Dial(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
	}
	return &Client{HTTP: &http.Client{Transport: tr}, base: "http://mishmar-signer"}
}

// Local is a client of a Manager's handler in this process.
func Local(h http.Handler) *Client {
	return &Client{HTTP: &http.Client{Transport: handlerTransport{h}}, base: "http://in-process"}
}

// Do makes a call: the method, the path (/apps/claude-code/propose), a body
// to send as JSON (nil for none), and where to decode the answer (nil to
// drop it). A refusal is a *signer.Error with the status and the code, as
// the engine's are.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderAPI, strconv.Itoa(APIVersion))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e api.Error
		if json.Unmarshal(raw, &e) != nil || e.Code == "" {
			e = api.Error{Code: "http_error", Message: fmt.Sprintf("the background signer answered %d", res.StatusCode)}
		}
		out := &signer.Error{Status: res.StatusCode, Code: e.Code, Message: e.Message}
		if e.Details != nil {
			out.Details = *e.Details
		}
		return out
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Health is GET /health.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	return h, c.Do(ctx, http.MethodGet, "/health", nil, &h)
}

// Shutdown is POST /shutdown.
func (c *Client) Shutdown(ctx context.Context, r ShutdownRequest) error {
	return c.Do(ctx, http.MethodPost, "/shutdown", r, nil)
}

// SignIn starts the app's sign-in, or answers the one waiting.
func (c *Client) SignIn(ctx context.Context, slug string, r SignInRequest) (SignIn, error) {
	var out SignIn
	return out, c.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(slug)+"/sign-in", r, &out)
}

// SignInState is the app's sign-in after waiting up to wait for it to end.
func (c *Client) SignInState(ctx context.Context, slug string, wait time.Duration) (SignIn, error) {
	var out SignIn
	return out, c.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(slug)+"/sign-in?wait="+strconv.Itoa(int(wait/time.Second)), nil, &out)
}

// Status is the app's status.
func (c *Client) Status(ctx context.Context, slug string) (Status, error) {
	var out Status
	return out, c.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(slug)+"/status", nil, &out)
}

// BriefStatus is the app's status without what needs the engine (its
// trading keys and account): the check before each tool.
func (c *Client) BriefStatus(ctx context.Context, slug string) (Status, error) {
	var out Status
	return out, c.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(slug)+"/status?brief=1", nil, &out)
}

// IsCode says whether err is the local API's refusal with that code.
func IsCode(err error, code string) bool {
	var e *signer.Error
	return errors.As(err, &e) && e.Code == code
}

// handlerTransport calls a handler in this process.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &recorder{header: http.Header{}, status: http.StatusOK}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	t.h.ServeHTTP(w, r)
	return &http.Response{
		StatusCode: w.status, Status: http.StatusText(w.status), Header: w.header, Request: r,
		Body: io.NopCloser(bytes.NewReader(w.body.Bytes())), ContentLength: int64(w.body.Len()),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
