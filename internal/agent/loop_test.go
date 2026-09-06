package agent

import (
	"testing"

	"loom/internal/config"
	"loom/internal/providers"
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
		if name == "powershell" {
			found = true
		}
	}
	if !found {
		t.Fatal("native tools must still be registered when an MCP server fails to start")
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
