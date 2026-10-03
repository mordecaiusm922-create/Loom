package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
)

func TestTruncateOutputKeepsHeadAndTail(t *testing.T) {
	out := "HEADER\n" + strings.Repeat("noise\n", 10_000) + "FATAL: connection refused\n"
	got := truncateOutput(out, 1000)
	if len(got) > 1200 {
		t.Fatalf("len = %d, want about 1000 plus the marker", len(got))
	}
	if !strings.HasPrefix(got, "HEADER") || !strings.Contains(got, "FATAL: connection refused") {
		t.Fatalf("head or tail lost:\n%s", got)
	}
	if !strings.Contains(got, "bytes truncated by Loom") {
		t.Fatal("missing truncation marker")
	}
}

func TestTruncateOutputLimits(t *testing.T) {
	small := "ok"
	if truncateOutput(small, 0) != small {
		t.Fatal("small output changed with the default limit")
	}
	big := strings.Repeat("x", config.DefaultMaxToolOutputBytes*2)
	if got := truncateOutput(big, 0); len(got) >= len(big) {
		t.Fatal("default limit not applied when limit is 0")
	}
	if got := truncateOutput(big, -1); got != big {
		t.Fatal("negative limit must disable truncation")
	}
}

func TestTruncateOutputNeverSplitsRunes(t *testing.T) {
	out := strings.Repeat("ñandú→", 5000)
	if got := truncateOutput(out, 999); !utf8.ValidString(got) {
		t.Fatal("truncation produced invalid UTF-8")
	}
}

// Regression: tool outputs had no cap, so one `kubectl logs` of a noisy pod
// could fill the model's context window by itself.
func TestExecuteCapsToolOutput(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{MaxToolOutputBytes: 500})
	if err := registry.Register(Tool{
		Name: "noisy",
		Run: func(context.Context, map[string]any) (string, error) {
			return strings.Repeat("log line\n", 10_000), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := registry.Execute(context.Background(), "noisy", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 700 {
		t.Fatalf("len(out) = %d, want the configured cap applied", len(out))
	}
}
