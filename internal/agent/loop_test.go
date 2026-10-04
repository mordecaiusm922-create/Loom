package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/providers"
)

func TestNewSessionDegradesPartiallyWhenAnMCPServerFails(t *testing.T) {
	t.Setenv("LOOM_UNSAFE_DISABLE_GOVERNANCE", "1")
	chdir(t, t.TempDir())

	cfg := config.Default()
	cfg.Governance.Enabled = false
	cfg.MCPServers = []config.MCPServerConfig{
		{Name: "does-not-exist", Command: "loom-mcp-server-that-does-not-exist"},
	}

	session, err := NewSession(cfg, "agent", "sess-mcp-degrade")
	if err != nil {
		t.Fatalf("NewSession: %v, want a bad MCP server to degrade, not fail the session", err)
	}
	defer session.Close()

	if len(session.MCPWarnings()) != 1 {
		t.Fatalf("MCPWarnings() = %v, want exactly 1 warning for the missing binary", session.MCPWarnings())
	}
	found := false
	for _, name := range session.ToolNames() {
		if name == cfg.Sre.ShellName() {
			found = true
		}
	}
	if !found {
		t.Fatal("native tools must still be registered when an MCP server fails to start")
	}
}

// scriptedProvider records every request and answers from a script.
type scriptedProvider struct {
	requests  []providers.CompletionRequest
	responses []*providers.CompletionResponse
	err       error
}

func (p *scriptedProvider) Name() string                    { return "scripted" }
func (p *scriptedProvider) SupportsTooling() bool           { return true }
func (p *scriptedProvider) CostPerMTok() (float64, float64) { return 0, 0 }
func (p *scriptedProvider) Complete(_ context.Context, req providers.CompletionRequest) (*providers.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func newScriptedSession(t *testing.T, provider *scriptedProvider) *Session {
	t.Helper()
	t.Setenv("LOOM_UNSAFE_DISABLE_GOVERNANCE", "1")
	chdir(t, t.TempDir())
	cfg := config.Default()
	cfg.Governance.Enabled = false
	session, err := NewSession(cfg, "agent", "sess-history")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	session.SetEventObserver(func(Event) {})
	session.resolve = func(config.Config, string) (providers.Provider, error) { return provider, nil }
	return session
}

// TestChatKeepsConversationAcrossTurns is the regression test for loom chat
// having no memory: every RunWithTask started from a single user message,
// so a follow-up ("and the other namespace?") reached the model with no
// idea what the previous turn was about.
func TestChatKeepsConversationAcrossTurns(t *testing.T) {
	provider := &scriptedProvider{responses: []*providers.CompletionResponse{
		{Text: "checkout has 3 pods in CrashLoopBackOff"},
		{Text: "payments is healthy"},
	}}
	session := newScriptedSession(t, provider)

	if err := session.RunWithTask(context.Background(), "check namespace checkout", "planning"); err != nil {
		t.Fatal(err)
	}
	if err := session.RunWithTask(context.Background(), "and payments?", "planning"); err != nil {
		t.Fatal(err)
	}

	second := provider.requests[1].Messages
	if len(second) != 3 {
		t.Fatalf("second turn sent %d messages, want 3 (user, assistant, user): %+v", len(second), second)
	}
	if second[0].Content != "check namespace checkout" || second[1].Role != "assistant" || second[1].Content != "checkout has 3 pods in CrashLoopBackOff" || second[2].Content != "and payments?" {
		t.Fatalf("second turn messages = %+v", second)
	}
}

func TestClearHistoryStartsFresh(t *testing.T) {
	provider := &scriptedProvider{responses: []*providers.CompletionResponse{{Text: "a"}, {Text: "b"}}}
	session := newScriptedSession(t, provider)
	_ = session.RunWithTask(context.Background(), "first", "planning")
	session.ClearHistory()
	_ = session.RunWithTask(context.Background(), "second", "planning")
	if got := provider.requests[1].Messages; len(got) != 1 || got[0].Content != "second" {
		t.Fatalf("after ClearHistory, messages = %+v, want only the new prompt", got)
	}
}

// A failed turn is kept, closed by an assistant note, so the next prompt
// keeps user/assistant alternation and the model knows the turn failed.
func TestFailedTurnKeepsAlternation(t *testing.T) {
	provider := &scriptedProvider{err: errors.New("rate limited")}
	session := newScriptedSession(t, provider)
	if err := session.RunWithTask(context.Background(), "first", "planning"); err == nil {
		t.Fatal("expected provider error")
	}
	provider.err = nil
	provider.responses = []*providers.CompletionResponse{{Text: "ok"}}
	if err := session.RunWithTask(context.Background(), "retry", "planning"); err != nil {
		t.Fatal(err)
	}
	got := provider.requests[1].Messages
	if len(got) != 3 || got[1].Role != "assistant" || !strings.Contains(got[1].Content, "rate limited") {
		t.Fatalf("messages = %+v, want user, assistant(interrupted), user", got)
	}
}

func TestResolveProviderUsesCustomProviderID(t *testing.T) {
	cfg := config.Default()
	cfg.CustomProviders = []config.CustomProviderConfig{{
		ID:              "groq",
		BaseURL:         "https://api.groq.com/openai/v1",
		Model:           "llama-3.3-70b-versatile",
		APIKeyEnv:       "GROQ_API_KEY",
		SupportsTooling: true,
	}}

	provider, err := resolveProvider(cfg, "groq")
	if err != nil {
		t.Fatal(err)
	}
	compatible, ok := provider.(*providers.OpenAICompatibleProvider)
	if !ok {
		t.Fatalf("provider type = %T, want OpenAICompatibleProvider", provider)
	}
	if compatible.Name() != "groq" || compatible.BaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("provider = %#v", compatible)
	}
}

func TestParsePlanStepsRoundTripsThroughGenericJSON(t *testing.T) {
	// Simulates what a provider actually hands us: tool call arguments
	// decoded from JSON into map[string]any, where nested arrays/objects
	// become []any/map[string]any, not typed Go structs.
	input := map[string]any{
		"steps": []any{
			map[string]any{"title": "Add Redis dependency", "status": "pending"},
			map[string]any{"title": "Create cache client", "status": "in_progress"},
			map[string]any{"title": "missing status field"},
			map[string]any{"status": "done"}, // missing title -> skipped
		},
	}
	steps := parsePlanSteps(input)
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3 (entries without a title must be skipped)", len(steps))
	}
	if steps[0].Title != "Add Redis dependency" || steps[0].Status != "pending" {
		t.Fatalf("steps[0] = %+v", steps[0])
	}
	if steps[1].Status != "in_progress" {
		t.Fatalf("steps[1] = %+v", steps[1])
	}
	if steps[2].Status != "" {
		t.Fatalf("steps[2] = %+v, want empty status when the model omits it", steps[2])
	}
}

func TestParsePlanStepsHandlesMissingOrWrongTypeGracefully(t *testing.T) {
	if steps := parsePlanSteps(map[string]any{}); len(steps) != 0 {
		t.Fatalf("expected 0 steps for missing key, got %d", len(steps))
	}
	if steps := parsePlanSteps(map[string]any{"steps": "not an array"}); len(steps) != 0 {
		t.Fatalf("expected 0 steps for wrong type, got %d", len(steps))
	}
}
