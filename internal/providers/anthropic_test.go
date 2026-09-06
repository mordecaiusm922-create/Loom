package providers

import "testing"

func TestEncodeAnthropicMessagesPreservesToolProtocol(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "lee el archivo"},
		{Role: "assistant", Content: "Lo leo.", ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Input: map[string]any{"path": "README.md"}}}},
		{Role: "tool", ToolCallID: "call-1", ToolName: "read_file", Content: "hola"},
	}
	encoded := encodeAnthropicMessages(messages)
	if len(encoded) != 3 {
		t.Fatalf("messages = %d, want 3", len(encoded))
	}
	if encoded[1].Content[1].Type != "tool_use" || encoded[1].Content[1].ID != "call-1" {
		t.Fatalf("assistant tool block = %#v", encoded[1].Content[1])
	}
	if encoded[2].Role != "user" || encoded[2].Content[0].Type != "tool_result" || encoded[2].Content[0].ToolUseID != "call-1" {
		t.Fatalf("tool result = %#v", encoded[2])
	}
}
