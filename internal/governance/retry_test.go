package governance

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
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	os.Exit(m.Run())
}

// A cold start answers 503 (or drops the connection) for a while and then
// recovers. The real decision must come through, not a spurious REVIEW.
func TestTransientFailureIsRetriedUntilDevMindAnswers(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"decision":"BLOCK","risk_score":98,"why":["hardblock:HB-005"]}`))
	}))
	defer server.Close()
	decision, err := NewDevMindEngine(server.URL, "t").EvaluateAction(context.Background(), "a", "bash", "execute", "rm -rf /", true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionValue != Block || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("decision=%q calls=%d, want BLOCK after 3 calls", decision.DecisionValue, calls)
	}
}

func TestPersistentFailureFailsClosedAfterRetries(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	decision, _ := NewDevMindEngine(server.URL, "t").EvaluateAction(context.Background(), "a", "bash", "execute", "ls", false)
	if decision.DecisionValue != Review {
		t.Fatalf("decision = %q, want REVIEW", decision.DecisionValue)
	}
	if got := atomic.LoadInt32(&calls); got != int32(len(retryBackoff)+1) {
		t.Fatalf("calls = %d, want %d", got, len(retryBackoff)+1)
	}
	if !strings.Contains(strings.Join(decision.Why, " "), "after 3 attempts") {
		t.Fatalf("why = %v, want the attempt count", decision.Why)
	}
}

// A rejected token will not fix itself: retrying only delays the answer.
func TestAuthFailureIsNotRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	decision, _ := NewDevMindEngine(server.URL, "bad").EvaluateAction(context.Background(), "a", "bash", "execute", "ls", false)
	if decision.DecisionValue != Review || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("decision=%q calls=%d, want REVIEW after 1 call", decision.DecisionValue, calls)
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
	defer func() { retryBackoff = []time.Duration{time.Millisecond, time.Millisecond} }()
	done := make(chan *EvaluateResponse, 1)
	go func() {
		d, _ := NewDevMindEngine(server.URL, "t").EvaluateAction(ctx, "a", "bash", "execute", "ls", false)
		done <- d
	}()
	select {
	case d := <-done:
		if d.DecisionValue != Review {
			t.Fatalf("decision = %q, want REVIEW", d.DecisionValue)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop ignored context cancellation")
	}
}
