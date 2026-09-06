package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatibleProviderToolCalls(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if _, ok := request["tools"]; !ok {
			t.Fatal("request does not include tools")
		}
		if string(request["tool_choice"]) != `"auto"` {
			t.Fatalf("tool_choice = %s, want auto", request["tool_choice"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "choices": [{
    "finish_reason": "tool_calls",
    "message": {
      "content": "Leyendo el archivo.",
      "tool_calls": [{
        "id": "call-1",
        "type": "function",
        "function": {"name": "read_file", "arguments": "{\"path\":\"README.md\"}"}
      }]
    }
  }]
}`))
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider("test-api", server.URL, "test-model", "TEST_OPENAI_KEY", true, 1.2, 3.4)
	response, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "lee README.md"}},
		Tools:    []ToolSpec{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "Leyendo el archivo." || len(response.ToolCalls) != 1 {
		t.Fatalf("response = %#v", response)
	}
	call := response.ToolCalls[0]
	if call.ID != "call-1" || call.Name != "read_file" || call.Input["path"] != "README.md" {
		t.Fatalf("tool call = %#v", call)
	}
	inputCost, outputCost := provider.CostPerMTok()
	if inputCost != 1.2 || outputCost != 3.4 {
		t.Fatalf("cost = (%f, %f)", inputCost, outputCost)
	}
}

// TestGeminiThoughtSignatureRoundTrips is the regression test for a real
// gap found in manual testing against gemini-3.5-flash: Gemini 3.x attaches
// an opaque extra_content.google.thought_signature to each function call in
// its response, and rejects the NEXT request with HTTP 400 ("Function call
// is missing a thought_signature") unless that exact blob is echoed back in
// the replayed assistant tool_calls. This provider doesn't need to
// understand the payload -- it only needs to round-trip it faithfully for
// whichever provider attaches one (a no-op for providers that don't, like
// Groq or a plain OpenAI-compatible server).
func TestGeminiThoughtSignatureRoundTrips(t *testing.T) {
	t.Setenv("TEST_GEMINI_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "choices": [{
    "finish_reason": "tool_calls",
    "message": {
      "content": "",
      "tool_calls": [{
        "id": "call-1",
        "type": "function",
        "function": {"name": "read_file", "arguments": "{\"path\":\"README.md\"}"},
        "extra_content": {"google": {"thought_signature": "opaque-blob-xyz"}}
      }]
    }
  }]
}`))
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider("google", server.URL, "gemini-3.5-flash", "TEST_GEMINI_KEY", true, 1.5, 9.0)
	response, err := provider.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "lee README.md"}},
		Tools:    []ToolSpec{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("response = %#v", response)
	}
	call := response.ToolCalls[0]
	if call.Extra == nil {
		t.Fatal("call.Extra is nil -- thought_signature was not captured from the response")
	}

	// Now replay it as an assistant message, the way the agent loop does,
	// and confirm the exact same extra_content comes back out unchanged.
	encoded := encodeOpenAIMessages([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{call}},
	})
	if len(encoded) != 1 || len(encoded[0].ToolCalls) != 1 {
		t.Fatalf("encoded = %#v", encoded)
	}
	roundTripped := encoded[0].ToolCalls[0].ExtraContent
	google, ok := roundTripped["google"].(map[string]any)
	if !ok || google["thought_signature"] != "opaque-blob-xyz" {
		t.Fatalf("extra_content did not round-trip unchanged: %#v", roundTripped)
	}
}

func TestEncodeOpenAIMessagesKeepsOneToolResultPerCall(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "lee dos archivos"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call-1", Name: "read_file", Input: map[string]any{"path": "one.txt"}},
			{ID: "call-2", Name: "read_file", Input: map[string]any{"path": "two.txt"}},
		}},
		{Role: "tool", ToolCallID: "call-1", Content: "one"},
		{Role: "tool", ToolCallID: "call-2", Content: "two"},
	}

	encoded := encodeOpenAIMessages(messages)
	if len(encoded) != 4 {
		t.Fatalf("messages = %d, want 4", len(encoded))
	}
	if len(encoded[1].ToolCalls) != 2 || encoded[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant tool calls = %#v", encoded[1].ToolCalls)
	}
	if encoded[2].Role != "tool" || encoded[2].ToolCallID != "call-1" {
		t.Fatalf("first tool result = %#v", encoded[2])
	}
	if encoded[3].Role != "tool" || encoded[3].ToolCallID != "call-2" {
		t.Fatalf("second tool result = %#v", encoded[3])
	}
}
