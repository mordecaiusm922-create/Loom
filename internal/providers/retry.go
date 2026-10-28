package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryBackoff spaces out retries of a model request the provider rejected as
// temporarily overloaded. Hosted models under a demand spike usually recover
// within seconds; without retries one spike fails the whole turn.
var retryBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// maxRetryAfter caps how long a provider's Retry-After header can stall a turn.
var maxRetryAfter = 30 * time.Second

// retryableStatus reports overload and gateway failures that are worth
// retrying: 429 rate limits, 502/503/504, and Anthropic's 529 "overloaded".
// Everything else (bad request, auth, unknown model) will not fix itself.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// doWithRetry sends the request built by newRequest and retries retryable
// statuses, honoring Retry-After when the provider sends one. Transport errors
// are not retried: a request that already timed out after two minutes should
// not silently take eight. It returns the last status and body, and the caller
// decides what a non-2xx status means.
func doWithRetry(ctx context.Context, client *http.Client, newRequest func() (*http.Request, error)) (status int, raw []byte, attempts int, err error) {
	for attempt := 0; ; attempt++ {
		req, err := newRequest()
		if err != nil {
			return 0, nil, attempt + 1, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, attempt + 1, err
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return 0, nil, attempt + 1, err
		}
		if !retryableStatus(resp.StatusCode) || attempt >= len(retryBackoff) {
			return resp.StatusCode, raw, attempt + 1, nil
		}
		wait := retryBackoff[attempt]
		if after, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			wait = after
		}
		select {
		case <-ctx.Done():
			return resp.StatusCode, raw, attempt + 1, nil
		case <-time.After(wait):
		}
	}
}

// parseRetryAfter reads the delay-seconds form of Retry-After, capped at
// maxRetryAfter. The HTTP-date form is rare from model APIs and falls back to
// retryBackoff.
func parseRetryAfter(value string) (time.Duration, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0, false
	}
	wait := time.Duration(seconds) * time.Second
	if wait > maxRetryAfter {
		wait = maxRetryAfter
	}
	return wait, true
}

// attemptsSuffix tells the operator a failure survived retries, so an
// overloaded provider is not mistaken for a broken config.
func attemptsSuffix(attempts int) string {
	if attempts <= 1 {
		return ""
	}
	return fmt.Sprintf(" (after %d attempts)", attempts)
}
