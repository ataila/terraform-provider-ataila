// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IdempotencyHeader carries the key that makes a create safe to retry.
const IdempotencyHeader = "Idempotency-Key"

const (
	// DefaultMaxRetries is how many times a retryable answer is retried.
	DefaultMaxRetries = 4
	// DefaultBackoffBase is the first backoff; each retry doubles it.
	DefaultBackoffBase = time.Second
	// DefaultBackoffMax caps one backoff.
	DefaultBackoffMax = 30 * time.Second
	// MaxRetryAfter is the longest Retry-After the provider waits for. A
	// longer one ends the retries and the error is returned.
	MaxRetryAfter = 5 * time.Minute

	maxErrorBody = 64 * 1024
)

// Retryable reports whether a status is retried. Only these four are: a rate
// limit and the three answers a proxy or the API gives while it is briefly
// unable to serve. Every other 4xx and 5xx is final, and so is a 503 whose
// code says a component is absent rather than busy (APIError.IsFinalUnavailable).
func Retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// transport implements HttpRequestDoer for the generated client.
type transport struct {
	hc          *http.Client
	userAgent   string
	maxRetries  int
	backoffBase time.Duration
	backoffMax  time.Duration

	// mu guards token, which a CI token renewal replaces while other requests
	// are in flight.
	mu    sync.Mutex
	token string
	// renew, when set, replaces an expired token (a CI token exchanged from a
	// GitLab ID token, API.ExchangeCIToken); renewMu makes concurrent requests
	// that all met the expiry renew it once.
	renew   func(ctx context.Context) error
	renewMu sync.Mutex

	// Seams for tests. newKey makes the Idempotency-Keys API.idempotencyKey
	// hands to the generated client.
	sleep  func(ctx context.Context, d time.Duration) error
	newKey func() string
}

// credentialFree marks a request context whose request must carry no
// Authorization header: the CI token exchange, whose proof is in its body.
type credentialFree struct{}

func withoutCredential(ctx context.Context) context.Context {
	return context.WithValue(ctx, credentialFree{}, true)
}

func (t *transport) currentToken() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token
}

func (t *transport) setToken(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = token
}

// renewFrom renews the token that `used` was, unless another request already
// did; it reports whether a request should be sent again.
func (t *transport) renewFrom(ctx context.Context, used string) bool {
	t.renewMu.Lock()
	defer t.renewMu.Unlock()
	if t.currentToken() != used {
		return true
	}
	return t.renew(ctx) == nil
}

func (t *transport) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("Accept", "application/json, "+ProblemContentType)
	// The Idempotency-Key is the contract's own header parameter, set by the
	// caller through the generated client (API.idempotencyKey); every retry
	// below sends this same request, and so the same key.

	ctx := req.Context()
	bare, _ := ctx.Value(credentialFree{}).(bool)
	renewed := false
	for attempt := 1; ; attempt++ {
		used := ""
		if bare {
			req.Header.Del("Authorization")
		} else {
			used = t.currentToken()
			req.Header.Set("Authorization", "Bearer "+used)
		}
		if attempt > 1 && req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, fmt.Errorf("%s %s: cannot retry, the request body is not replayable", req.Method, req.URL.Path)
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("%s %s: replaying the request body: %w", req.Method, req.URL.Path, err)
			}
			req.Body = body
		}

		resp, err := t.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
		apiErr := newAPIError(resp, body, attempt)

		// A CI token lives an hour; an apply may outlast it. Exchange the
		// GitLab ID token again, once per request, and send the request anew.
		if resp.StatusCode == http.StatusUnauthorized && apiErr.Code() == "token_expired" &&
			!bare && !renewed && t.renew != nil {
			renewed = true
			if t.renewFrom(ctx, used) {
				continue
			}
		}
		if !Retryable(resp.StatusCode) || attempt > t.maxRetries || apiErr.IsFinalUnavailable() {
			return nil, apiErr
		}
		wait, ok := t.delay(attempt, resp.Header.Get("Retry-After"))
		if !ok {
			return nil, apiErr
		}
		if err := t.sleep(ctx, wait); err != nil {
			return nil, apiErr
		}
	}
}

// delay is the wait before retry number `attempt`. Retry-After wins when the
// API sends one; false means the API asked for longer than MaxRetryAfter.
func (t *transport) delay(attempt int, retryAfter string) (time.Duration, bool) {
	if d, ok := parseRetryAfter(retryAfter, time.Now()); ok {
		if d > MaxRetryAfter {
			return 0, false
		}
		return d, true
	}
	backoff := float64(t.backoffBase) * math.Pow(2, float64(attempt-1))
	if backoff > float64(t.backoffMax) {
		backoff = float64(t.backoffMax)
	}
	// Up to 20% jitter so parallel runs do not retry in lockstep.
	jitter := backoff * 0.2 * mrand.Float64()
	return time.Duration(backoff + jitter), true
}

// parseRetryAfter reads delay-seconds or an HTTP-date (RFC 9110 §10.2.3).
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		d := at.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewIdempotencyKey returns a random version 4 UUID.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
