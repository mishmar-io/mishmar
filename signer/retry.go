package signer

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Retries is how a client retries a call the engine did nothing with: 503
// unavailable while the engine restarts (its drain is 28 s), or 429
// rate_limited. The contract promises both did nothing, so the same request
// goes again: after the seconds in Retry-After when the engine names them,
// otherwise after a wait that doubles from MinWait to MaxWait with jitter,
// until Budget has passed since the first attempt. Then the last answer is
// the error. Nothing else is retried: a 413 too_large would come back the
// same, and any other answer means the request was taken.
//
// The zero value is the default policy, so retries are on unless Disabled.
type Retries struct {
	// Disabled turns retries off: one attempt, whatever the answer.
	Disabled bool
	// Budget is how long after the first attempt a retry may still start;
	// 0 means DefaultRetryBudget. A wait that would end past it is not
	// taken.
	Budget time.Duration
	// MinWait and MaxWait bound the wait when the engine sets no
	// Retry-After; 0 means DefaultRetryMinWait and DefaultRetryMaxWait.
	MinWait, MaxWait time.Duration
}

// The default policy: 45 s covers the engine's 28 s drain and its restart.
const (
	DefaultRetryBudget  = 45 * time.Second
	DefaultRetryMinWait = 250 * time.Millisecond
	DefaultRetryMaxWait = 8 * time.Second
)

// Retryable reports whether an answer with this status means the request
// did nothing and may go again: 503 and 429, whatever the body.
func Retryable(status int) bool {
	return status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests
}

// Wait is how long to wait before attempt+1 (attempt counts from 0) after an
// answer with the status and Retry-After header, elapsed after the first
// attempt began; false means stop and return the answer as the error.
func (r Retries) Wait(attempt, status int, retryAfter string, elapsed time.Duration) (time.Duration, bool) {
	if r.Disabled || !Retryable(status) {
		return 0, false
	}
	budget, minWait, maxWait := r.Budget, r.MinWait, r.MaxWait
	if budget <= 0 {
		budget = DefaultRetryBudget
	}
	if minWait <= 0 {
		minWait = DefaultRetryMinWait
	}
	if maxWait < minWait {
		maxWait = max(DefaultRetryMaxWait, minWait)
	}
	wait, named := parseRetryAfter(retryAfter)
	if !named {
		// Doubling from MinWait, capped at MaxWait, between half and all of
		// the step so that clients that failed together do not come back
		// together.
		step := min(minWait<<uint(min(attempt, 20)), maxWait)
		wait = step/2 + time.Duration(rand.Int64N(int64(step/2)+1))
	}
	if elapsed+wait > budget {
		return 0, false
	}
	return wait, true
}

// parseRetryAfter reads a Retry-After header: seconds, or an HTTP date.
func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}
