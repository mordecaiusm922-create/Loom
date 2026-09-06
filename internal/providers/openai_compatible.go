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

// OpenAICompatibleProvider works with any server implementing the OpenAI Chat
// Completions protocol, including hosted APIs and local inference servers.
// ProviderName is named differently from Provider.Name() because Go does not
// permit a field and method with the same name.
type OpenAICompatibleProvider struct {
	ProviderName        string
	BaseURL             string
	Model               string
	APIKeyEnv           string
	SupportsToolingFlag bool
	CostIn              float64
	CostOut             float64
	client              *http.Client
}

func NewOpenAICompatibleProvider(
	providerName, baseURL, model, apiKeyEnv string,
	supportsTooling bool,
	costIn, costOut float64,
) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{
		ProviderName:        providerName,
		BaseURL:             strings.TrimRight(baseURL, "/"),
		Model:               model,
		APIKeyEnv:           apiKeyEnv,
		SupportsToolingFlag: supportsTooling,
		CostIn:              costIn,
		CostOut:             costOut,
		client:              &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAICompatibleProvider) Name() string {
	return p.ProviderName
}

func (p *OpenAICompatibleProvider) SupportsTooling() bool {
	return p.SupportsToolingFlag
}

func (p *OpenAICompatibleProvider) CostPerMTok() (float64, float64) {
	return p.CostIn, p.CostOut
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
	// ExtraContent carries provider-specific extensions attached to a
	// single tool call -- most notably Gemini 3's
	// extra_content.google.thought_signature, which MUST be echoed back
	// unchanged on the next request or multi-turn tool calling breaks
	// with HTTP 400 ("Function call is missing a thought_signature").
	// Loom does not need to understand this payload's shape: it just
	// round-trips whatever the provider sent, for whichever provider
	// requires it.
	ExtraContent map[string]any `json:"extra_content,omitempty"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type openAIRequest struct {
	Model      string           `json:"model"`
	Messages   []openAIMessage  `json:"messages"`
	Tools      []openAIToolSpec `json:"tools,omitempty"`
	ToolChoice string           `json:"tool_choice,omitempty"`
	MaxTokens  int              `json:"max_tokens"`
}

type openAIResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (p *OpenAICompatibleProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for provider %q", p.ProviderName)
	}
	if p.Model == "" {
		return nil, fmt.Errorf("model is required for provider %q", p.ProviderName)
	}
	if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
		return nil, fmt.Errorf("environment variable %s is required for provider %q", p.APIKeyEnv, p.ProviderName)
	}

	tools := make([]openAIToolSpec, 0, len(req.Tools))
	if p.SupportsToolingFlag {
		for _, tool := range req.Tools {
			var spec openAIToolSpec
			spec.Type = "function"
			spec.Function.Name = tool.Name
			spec.Function.Description = tool.Description
			spec.Function.Parameters = tool.InputSchema
			tools = append(tools, spec)
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	messages := encodeOpenAIMessages(req.Messages)
	if req.SystemPrompt != "" {
		messages = append([]openAIMessage{{Role: "system", Content: req.SystemPrompt}}, messages...)
	}
	body, err := json.Marshal(openAIRequest{
		Model:      p.Model,
		Messages:   messages,
		Tools:      tools,
		ToolChoice: toolChoice(p.SupportsToolingFlag),
		MaxTokens:  maxTokens,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(p.BaseURL, "/")+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKeyEnv != "" {
		httpReq.Header.Set("Authorization", "Bearer "+os.Getenv(p.APIKeyEnv))
	}
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
		return nil, fmt.Errorf("OpenAI-compatible provider returned HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var decoded openAIResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("invalid OpenAI-compatible response: %s", string(raw))
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("OpenAI-compatible provider error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("OpenAI-compatible response contains no choices")
	}

	choice := decoded.Choices[0]
	out := &CompletionResponse{Text: choice.Message.Content, StopReason: choice.FinishReason, Usage: Usage{InputTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens}}
	for _, call := range choice.Message.ToolCalls {
		arguments := map[string]any{}
		if call.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
				return nil, fmt.Errorf("invalid tool arguments for %q: %w", call.Function.Name, err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Input: arguments, Extra: call.ExtraContent})
	}
	return out, nil
}

func toolChoice(supportsTooling bool) string {
	if supportsTooling {
		return "auto"
	}
	return ""
}

// encodeOpenAIMessages preserves one result message per tool_call_id. Unlike
// Anthropic, OpenAI-compatible APIs do not merge multiple tool results.
func encodeOpenAIMessages(messages []Message) []openAIMessage {
	encoded := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		item := openAIMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			arguments, err := json.Marshal(call.Input)
			if err != nil {
				arguments = []byte("{}")
			}
			item.ToolCalls = append(item.ToolCalls, openAIToolCall{
				ID:   call.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      call.Name,
					Arguments: string(arguments),
				},
				ExtraContent: call.Extra,
			})
		}
		encoded = append(encoded, item)
	}
	return encoded
}
