package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaProvider uses the local /api/chat endpoint. Recent Ollama models can
// call tools natively, giving local work the same governed execution path.
type OllamaProvider struct {
	BaseURL string
	Model   string
	client  *http.Client
}

func NewOllamaProvider(model, baseURL string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	return &OllamaProvider{BaseURL: baseURL, Model: model, client: &http.Client{Timeout: 300 * time.Second}}
}

func (p *OllamaProvider) Name() string                    { return "local/" + p.Model }
func (p *OllamaProvider) SupportsTooling() bool           { return true }
func (p *OllamaProvider) CostPerMTok() (float64, float64) { return 0, 0 }

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type ollamaChatRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Tools    []ollamaToolSpec `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	Error   string        `json:"error,omitempty"`
	// Ollama reports prompt and generated token counts under these names.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (p *OllamaProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	messages := make([]ollamaMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, message := range req.Messages {
		encoded := ollamaMessage{Role: message.Role, Content: message.Content, ToolName: message.ToolName}
		for _, call := range message.ToolCalls {
			encoded.ToolCalls = append(encoded.ToolCalls, ollamaToolCall{Function: ollamaFunctionCall{Name: call.Name, Arguments: call.Input}})
		}
		messages = append(messages, encoded)
	}
	tools := make([]ollamaToolSpec, 0, len(req.Tools))
	for _, tool := range req.Tools {
		var spec ollamaToolSpec
		spec.Type = "function"
		spec.Function.Name = tool.Name
		spec.Function.Description = tool.Description
		spec.Function.Parameters = tool.InputSchema
		tools = append(tools, spec)
	}
	body, err := json.Marshal(ollamaChatRequest{Model: p.Model, Messages: messages, Tools: tools, Stream: false})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 300 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("no se pudo contactar Ollama en %s: %w", p.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama devolvio HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var decoded ollamaChatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de Ollama: %s", string(raw))
	}
	if decoded.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", decoded.Error)
	}
	out := &CompletionResponse{Text: decoded.Message.Content, StopReason: "stop", Usage: Usage{InputTokens: decoded.PromptEvalCount, OutputTokens: decoded.EvalCount}}
	for index, call := range decoded.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: fmt.Sprintf("ollama-%d", index+1), Name: call.Function.Name, Input: call.Function.Arguments})
	}
	return out, nil
}
