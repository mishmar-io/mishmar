package signer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// flaky is an engine that answers a scripted status per attempt, then 200
// with an empty list, and remembers each attempt's nonce header and, when
// asked, its body.
type flaky struct {
	mu       sync.Mutex
	script   []int    // the status of each attempt, in order; past the end, 200
	headers  []string // Retry-After per scripted attempt, "" for none
	bodies   bool
	nonces   []string
	seen     []string
	attempts int
}

func (f *flaky) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := f.attempts
		f.attempts++
		f.nonces = append(f.nonces, r.Header.Get(agentkey.HeaderNonce))
		if f.bodies {
			raw, _ := io.ReadAll(r.Body)
			f.seen = append(f.seen, string(raw))
		}
		if i < len(f.script) {
			if i < len(f.headers) && f.headers[i] != "" {
				w.Header().Set("Retry-After", f.headers[i])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.script[i])
			code := map[int]string{503: "unavailable", 429: "rate_limited", 413: "too_large"}[f.script[i]]
			if code == "" {
				code = "other"
			}
			_, _ = w.Write([]byte(`{"code":"` + code + `","message":"scripted"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
}

// retryClient is a client on the flaky engine whose waits are recorded, not slept.
func retryClient(t *testing.T, f *flaky, r Retries) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	var waits []time.Duration
	c := NewClient(srv.URL+"/v1", "key_a", agentkey.FromSeed(testSeed(1)))
	c.Retries = r
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return c, &waits
}

func TestRetriesAnAnswerThatDidNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("503 twice then 200, each attempt signed anew", func(t *testing.T) {
		f := &flaky{script: []int{503, 503}}
		c, waits := retryClient(t, f, Retries{})
		if _, err := c.Positions(ctx); err != nil {
			t.Fatalf("after two 503s: %v", err)
		}
		if f.attempts != 3 || len(*waits) != 2 {
			t.Fatalf("%d attempts, %d waits; want 3 and 2", f.attempts, len(*waits))
		}
		for i, w := range *waits {
			if w < DefaultRetryMinWait/2 || w > DefaultRetryMaxWait {
				t.Fatalf("wait %d of %s is outside the policy", i, w)
			}
		}
		if (*waits)[1] < (*waits)[0]/2 {
			t.Fatalf("waits %v do not grow", *waits)
		}
		if f.nonces[0] == f.nonces[1] || f.nonces[1] == f.nonces[2] {
			t.Fatalf("a retry reused a nonce: %v", f.nonces)
		}
	})

	t.Run("429 waits the seconds in Retry-After", func(t *testing.T) {
		f := &flaky{script: []int{429}, headers: []string{"3"}}
		c, waits := retryClient(t, f, Retries{})
		if _, err := c.Positions(ctx); err != nil {
			t.Fatalf("after a 429: %v", err)
		}
		if len(*waits) != 1 || (*waits)[0] != 3*time.Second {
			t.Fatalf("waits %v, want one of 3 s", *waits)
		}
	})

	t.Run("a Retry-After past the budget is not waited for", func(t *testing.T) {
		f := &flaky{script: []int{429}, headers: []string{"120"}}
		c, waits := retryClient(t, f, Retries{})
		_, err := c.Positions(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 429 || e.Code != "rate_limited" {
			t.Fatalf("want the 429 back, got %v", err)
		}
		if f.attempts != 1 || len(*waits) != 0 {
			t.Fatalf("%d attempts, %d waits; want 1 and 0", f.attempts, len(*waits))
		}
	})

	t.Run("413 is not retried", func(t *testing.T) {
		f := &flaky{script: []int{413}}
		c, waits := retryClient(t, f, Retries{})
		_, err := c.Positions(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 413 || e.Code != "too_large" {
			t.Fatalf("want the 413 back, got %v", err)
		}
		if f.attempts != 1 || len(*waits) != 0 {
			t.Fatalf("%d attempts, %d waits; want 1 and 0", f.attempts, len(*waits))
		}
	})

	t.Run("the budget runs out and the last answer is the error", func(t *testing.T) {
		f := &flaky{script: []int{503, 503, 503, 503, 503, 503, 503, 503}}
		// A budget shorter than the first wait: one attempt, no retry.
		c, waits := retryClient(t, f, Retries{Budget: time.Millisecond, MinWait: 10 * time.Millisecond, MaxWait: 20 * time.Millisecond})
		_, err := c.Positions(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 503 || e.Code != "unavailable" {
			t.Fatalf("want the 503 back, got %v", err)
		}
		if f.attempts != 1 || len(*waits) != 0 {
			t.Fatalf("%d attempts, %d waits; want 1 and 0", f.attempts, len(*waits))
		}
	})

	t.Run("disabled: one attempt", func(t *testing.T) {
		f := &flaky{script: []int{503}}
		c, waits := retryClient(t, f, Retries{Disabled: true})
		_, err := c.Positions(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 503 {
			t.Fatalf("want the 503 back, got %v", err)
		}
		if f.attempts != 1 || len(*waits) != 0 {
			t.Fatalf("%d attempts, %d waits; want 1 and 0", f.attempts, len(*waits))
		}
	})

	t.Run("a cancelled context ends the wait and keeps the engine's answer", func(t *testing.T) {
		f := &flaky{script: []int{503, 503}}
		c, _ := retryClient(t, f, Retries{})
		cctx, cancel := context.WithCancel(ctx)
		c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return context.Canceled }
		_, err := c.Positions(cctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 503 || !errors.Is(err, context.Canceled) {
			t.Fatalf("want the 503 and the cancellation, got %v", err)
		}
	})

	t.Run("a proposal goes again too, the same bytes", func(t *testing.T) {
		f := &flaky{script: []int{503}, bodies: true}
		c, waits := retryClient(t, f, Retries{})
		price := "60000"
		// The flaky engine answers [] to the retried POST, which is not a
		// decision; what matters is the second attempt after a wait, with the
		// body the first one carried.
		_, _ = c.Propose(ctx, api.ProposalSubmit{
			TradingAccountId: "rt_1", Kind: api.ProposalKindOrder, Symbol: "BTCUSDT", Side: api.SideBuy, Size: "0.5", Price: &price,
			OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: "n1", ProposedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		})
		if f.attempts != 2 || len(*waits) != 1 {
			t.Fatalf("%d attempts, %d waits; want 2 and 1", f.attempts, len(*waits))
		}
		if len(f.seen) != 2 || f.seen[0] != f.seen[1] || f.seen[0] == "" {
			t.Fatalf("the retried proposal differs from the first: %d bodies", len(f.seen))
		}
	})
}

func TestRetriesWait(t *testing.T) {
	r := Retries{}
	if _, again := r.Wait(0, 500, "", 0); again {
		t.Fatal("a 500 is not retried")
	}
	if _, again := r.Wait(0, 503, "", DefaultRetryBudget); again {
		t.Fatal("past the budget nothing is retried")
	}
	w, again := r.Wait(0, 503, "", 0)
	if !again || w < DefaultRetryMinWait/2 || w > DefaultRetryMinWait {
		t.Fatalf("first wait %s, want within half of and all of %s", w, DefaultRetryMinWait)
	}
	w, again = r.Wait(30, 503, "", 0)
	if !again || w > DefaultRetryMaxWait || w < DefaultRetryMaxWait/2 {
		t.Fatalf("late wait %s, want capped at %s", w, DefaultRetryMaxWait)
	}
	date := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	w, again = r.Wait(0, 429, date, 0)
	if !again || w <= 0 || w > 2*time.Second {
		t.Fatalf("an HTTP date in Retry-After gave %s", w)
	}
	w, again = r.Wait(0, 429, "garbage", 0)
	if !again || w <= 0 {
		t.Fatalf("a bad Retry-After falls back to the backoff, got %s %v", w, again)
	}
}
