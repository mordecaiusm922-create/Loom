package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// AnthropicProvider talks directly to the Messages API.
type AnthropicProvider struct {
	APIKey string
	Model  string
	// BaseURL permits an API gateway or a local test server. Empty uses Anthropic.
	BaseURL string
	client  *http.Client
}

func NewAnthropicProvider(model string) (*AnthropicProvider, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY no esta configurada")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &AnthropicProvider{APIKey: key, Model: model, client: &http.Client{Timeout: 120 * time.Second}}, nil
}

func (p *AnthropicProvider) Name() string                    { return "anthropic/" + p.Model }
func (p *AnthropicProvider) SupportsTooling() bool           { return true }
func (p *AnthropicProvider) CostPerMTok() (float64, float64) { return 3.0, 15.0 }

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
	Tools     []anthropicToolSpec `json:"tools,omitempty"`
}

type anthropicToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	tools := make([]anthropicToolSpec, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, anthropicToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	body, err := json.Marshal(anthropicRequest{Model: p.Model, MaxTokens: maxTokens, System: req.SystemPrompt, Messages: encodeAnthropicMessages(req.Messages), Tools: tools})
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(p.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic returned HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var decoded anthropicResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de Anthropic: %s", string(raw))
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("anthropic error: %s", decoded.Error.Message)
	}
	out := &CompletionResponse{StopReason: decoded.StopReason, Usage: Usage{InputTokens: decoded.Usage.InputTokens, OutputTokens: decoded.Usage.OutputTokens}}
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			out.Text += block.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Input: block.Input})
		}
	}
	return out, nil
}

// Anthropic uses assistant tool_use blocks followed by user tool_result
// blocks. Keeping this translation here makes the generic agent loop correct
// for more than one tool-call turn.
func encodeAnthropicMessages(messages []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "tool":
			result := anthropicContentBlock{Type: "tool_result", ToolUseID: message.ToolCallID, Content: message.Content}
			if len(out) > 0 && out[len(out)-1].Role == "user" && onlyToolResults(out[len(out)-1].Content) {
				out[len(out)-1].Content = append(out[len(out)-1].Content, result)
			} else {
				out = append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{result}})
			}
		case "assistant":
			blocks := make([]anthropicContentBlock, 0, len(message.ToolCalls)+1)
			if message.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: message.Content})
			}
			for _, call := range message.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: call.Input})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			}
		default:
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{{Type: "text", Text: message.Content}}})
		}
	}
	return out
}

func onlyToolResults(blocks []anthropicContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}
