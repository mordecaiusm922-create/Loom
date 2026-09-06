package providers

import "context"

// ToolSpec describes a tool available to a provider.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is a provider request to invoke a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
	// Extra carries opaque, provider-specific data attached to this call
	// that must be echoed back verbatim on the next request for
	// multi-turn tool calling to keep working -- e.g. Gemini 3's
	// per-function-call "thought signature". Nil for providers that
	// don't need this (Anthropic, Groq, most others).
	Extra map[string]any
}

// CompletionRequest is provider-neutral.
type CompletionRequest struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolSpec
	MaxTokens    int
}

// Message is the provider-neutral conversation record. ToolCalls must stay on
// the assistant message so native provider protocols can reconstruct tool_use.
type Message struct {
	Role       string // "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
}

type CompletionResponse struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Provider is the runtime contract for model backends.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
	SupportsTooling() bool
	CostPerMTok() (input float64, output float64)
}
