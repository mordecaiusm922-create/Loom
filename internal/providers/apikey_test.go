package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIKeyIsTrimmed(t *testing.T) {
	t.Setenv("TEST_KEY", "  sk-or-v1-abc\r\n")
	key, err := apiKeyFromEnv("TEST_KEY")
	if err != nil || key != "sk-or-v1-abc" {
		t.Fatalf("key = %q, %v; want the trimmed key", key, err)
	}
}

// A key pasted into Windows PowerShell 5.1 with Ctrl+V can carry control
// characters; it must be reported by name, never echoed.
func TestAPIKeyWithControlCharactersIsRejectedWithoutEchoingIt(t *testing.T) {
	for _, bad := range []string{"sk-or-v1-\x16abc", "sk-or v1", "sk-ñ"} {
		t.Setenv("TEST_KEY", bad)
		_, err := apiKeyFromEnv("TEST_KEY")
		if err == nil || !strings.Contains(err.Error(), "TEST_KEY") || strings.Contains(err.Error(), bad) {
			t.Fatalf("%q: err = %v; want a named error that does not contain the key", bad, err)
		}
	}
	t.Setenv("TEST_KEY", "   ")
	if _, err := apiKeyFromEnv("TEST_KEY"); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("blank key: err = %v, want not set", err)
	}
}

func TestRejectedKeyIsExplainedWithoutTheResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-v1-wrong" {
			t.Errorf("Authorization = %q, want the trimmed key", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Missing Authentication header","code":401}}`))
	}))
	defer server.Close()
	t.Setenv("TEST_KEY", "sk-or-v1-wrong\n")
	p := NewOpenAICompatibleProvider("openrouter", server.URL, "m", "TEST_KEY", true, 0, 0)
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "rejected the API key in TEST_KEY (HTTP 401)") {
		t.Fatalf("err = %v, want a clear rejected-key error", err)
	}
	if strings.Contains(err.Error(), "Missing Authentication header") || strings.Contains(err.Error(), "sk-or-v1-wrong") {
		t.Fatalf("err leaks the body or the key: %v", err)
	}
}

func TestExpectedKeyPrefix(t *testing.T) {
	cases := map[string]string{
		"https://openrouter.ai/api/v1":                            "sk-or-",
		"https://api.anthropic.com":                               "sk-ant-",
		"https://generativelanguage.googleapis.com/v1beta/openai": "AIza",
		"http://localhost:8000/v1":                                "",
	}
	for base, want := range cases {
		if got := ExpectedKeyPrefix(base); got != want {
			t.Fatalf("ExpectedKeyPrefix(%q) = %q, want %q", base, got, want)
		}
	}
}
