package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// operatorClient is the little of the operator API that setting an agent up
// needs, with a bearer token. It lives here and not in the signer package:
// the signer never holds an operator token.
type operatorClient struct {
	base  string
	token string
	http  *http.Client
}

func newOperatorClient(engine, token string) *operatorClient {
	return &operatorClient{base: strings.TrimRight(engine, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *operatorClient) getTradingAccount(ctx context.Context, id string) (api.TradingAccount, error) {
	var r api.TradingAccount
	err := c.do(ctx, http.MethodGet, "/trading-accounts/"+url.PathEscape(id), nil, &r)
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
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e api.Error
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return fmt.Errorf("engine refused %s %s (%d %s): %s", method, path, res.StatusCode, e.Code, e.Message)
		}
		return fmt.Errorf("engine answered %s %s with %d: %s", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: unexpected response: %w", method, path, err)
		}
	}
	return nil
}
