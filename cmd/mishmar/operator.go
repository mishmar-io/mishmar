package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// operatorClient is the little of the operator API that setting an agent up
// needs, with a bearer token. It lives here and not in the signer package:
// the signer never holds an operator token. A call the engine did nothing
// with (503 while it restarts, 429) goes again under the signer client's
// default policy.
type operatorClient struct {
	base    string
	token   string
	http    *http.Client
	retries signer.Retries
}

func newOperatorClient(engine, token string) *operatorClient {
	return &operatorClient{base: strings.TrimRight(engine, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *operatorClient) getConnectedAccount(ctx context.Context, id string) (api.ConnectedAccount, error) {
	var r api.ConnectedAccount
	err := c.do(ctx, http.MethodGet, "/connected-accounts/"+url.PathEscape(id), nil, &r)
	return r, err
}

func (c *operatorClient) createAgent(ctx context.Context, in api.AgentCreate) (api.Agent, error) {
	var a api.Agent
	err := c.do(ctx, http.MethodPost, "/agents", in, &a)
	return a, err
}

func (c *operatorClient) issueKey(ctx context.Context, agentID string) (api.AgentKeyIssued, error) {
	var k api.AgentKeyIssued
	err := c.do(ctx, http.MethodPost, "/agents/"+url.PathEscape(agentID)+"/keys", nil, &k)
	return k, err
}

func (c *operatorClient) createVenueKey(ctx context.Context, agentID string, in api.VenueKeyCreate) (api.VenueKey, error) {
	var k api.VenueKey
	err := c.do(ctx, http.MethodPost, "/agents/"+url.PathEscape(agentID)+"/venue-keys", in, &k)
	return k, err
}

func (c *operatorClient) do(ctx context.Context, method, path string, in, out any) error {
	var encoded []byte
	if in != nil {
		var err error
		if encoded, err = json.Marshal(in); err != nil {
			return err
		}
	}
	start := time.Now()
	for attempt := 0; ; attempt++ {
		res, raw, err := c.once(ctx, method, path, encoded)
		if err != nil {
			return err
		}
		if res.StatusCode >= 200 && res.StatusCode <= 299 {
			if out != nil && len(bytes.TrimSpace(raw)) > 0 {
				if err := json.Unmarshal(raw, out); err != nil {
					return fmt.Errorf("%s %s: unexpected response: %w", method, path, err)
				}
			}
			return nil
		}
		var e api.Error
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			err = fmt.Errorf("engine refused %s %s (%d %s): %s", method, path, res.StatusCode, e.Code, e.Message)
		} else {
			err = fmt.Errorf("engine answered %s %s with %d: %s", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
		}
		wait, again := c.retries.Wait(attempt, res.StatusCode, res.Header.Get("Retry-After"), time.Since(start))
		if !again {
			return err
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		}
	}
}

// once sends one request and reads its answer, whatever the status.
func (c *operatorClient) once(ctx context.Context, method, path string, encoded []byte) (*http.Response, []byte, error) {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if encoded != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res, raw, nil
}
