package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	os.Exit(m.Run())
}

const okCompletion = `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hola"}}]}`

func newRetryProvider(url string) *OpenAICompatibleProvider {
	return NewOpenAICompatibleProvider("test", url, "m", "", true, 0, 0)
}

// A demand spike answers 503 for a while and then recovers. The turn must get
// the real answer instead of failing on the first overloaded response.
func TestOverloadedProviderIsRetriedUntilItAnswers(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(okCompletion))
	}))
	defer server.Close()
	resp, err := newRetryProvider(server.URL).Complete(context.Background(), CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hola" || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("text=%q calls=%d, want hola after 3 calls", resp.Text, calls)
	}
}

func TestRateLimitIsRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(okCompletion))
	}))
	defer server.Close()
	if _, err := newRetryProvider(server.URL).Complete(context.Background(), CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestPersistentOverloadFailsWithAttemptCount(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"high demand"}}`))
	}))
	defer server.Close()
	_, err := newRetryProvider(server.URL).Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("want an error after the retries run out")
	}
	if got := atomic.LoadInt32(&calls); got != int32(len(retryBackoff)+1) {
		t.Fatalf("calls = %d, want %d", got, len(retryBackoff)+1)
	}
	if !strings.Contains(err.Error(), "HTTP 503 (after 4 attempts)") || !strings.Contains(err.Error(), "high demand") {
		t.Fatalf("err = %v, want the status, attempt count and provider message", err)
	}
}

// An unknown model or a rejected key will not fix itself: retrying only
// delays the answer.
func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(code)
		}))
		_, err := newRetryProvider(server.URL).Complete(context.Background(), CompletionRequest{})
		server.Close()
		if err == nil || atomic.LoadInt32(&calls) != 1 {
			t.Fatalf("HTTP %d: err=%v calls=%d, want an error after 1 call", code, err, calls)
		}
		if strings.Contains(err.Error(), "attempts") {
			t.Fatalf("HTTP %d: err = %v, a single attempt must not report an attempt count", code, err)
		}
	}
}

func TestAnthropicOverloadedIsRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(529)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hola"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()
	p := &AnthropicProvider{APIKey: "k", Model: "m", BaseURL: server.URL}
	resp, err := p.Complete(context.Background(), CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hola" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("text=%q calls=%d, want hola after 2 calls", resp.Text, calls)
	}
}

// Retry-After overrides the default backoff, so a provider that asks for an
// immediate retry is not made to wait an hour.
func TestRetryAfterOverridesBackoff(t *testing.T) {
	retryBackoff = []time.Duration{time.Hour}
	defer func() { retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond} }()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(okCompletion))
	}))
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		_, err := newRetryProvider(server.URL).Complete(context.Background(), CompletionRequest{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Retry-After: 0 was ignored")
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	if wait, ok := parseRetryAfter("3600"); !ok || wait != maxRetryAfter {
		t.Fatalf("wait=%v ok=%v, want %v", wait, ok, maxRetryAfter)
	}
	if _, ok := parseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT"); ok {
		t.Fatal("the HTTP-date form must fall back to the default backoff")
	}
}

func TestCancelledContextStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	retryBackoff = []time.Duration{time.Hour}
	defer func() { retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond} }()
	done := make(chan error, 1)
	go func() {
		_, err := newRetryProvider(server.URL).Complete(ctx, CompletionRequest{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error once the context is cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop ignored context cancellation")
	}
}
