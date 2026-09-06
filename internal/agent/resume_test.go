package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSessionLog(t *testing.T, dir, sessionID, content string) {
	t.Helper()
	sessionsDir := filepath.Join(dir, ".loom", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sessionsDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
}

func TestResumeContextSummarizesToolsAndSessionSummary(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	writeSessionLog(t, dir, "sess-1", strings.Join([]string{
		`{"Time":"2026-08-15T10:00:00Z","SessionID":"sess-1","Tool":"k8s_get","Engine":"policy","Decision":"ALLOW","RiskScore":1,"Executed":true,"Outcome":"completed"}`,
		`{"Time":"2026-08-15T10:01:00Z","SessionID":"sess-1","Tool":"powershell","Engine":"infra","Decision":"BLOCK","RiskScore":9,"Executed":false,"Outcome":"blocked"}`,
		`{"event_type":"session_summary","time":"2026-08-15T10:02:00Z","session_id":"sess-1","provider":"anthropic","input_tokens":100,"output_tokens":50,"estimated_cost_usd":0.01,"duration_ms":1500}`,
	}, "\n"))

	summary, err := ResumeContext("sess-1")
	if err != nil {
		t.Fatalf("ResumeContext: %v", err)
	}
	if !strings.Contains(summary, "k8s_get") || !strings.Contains(summary, "decision=ALLOW") {
		t.Fatalf("summary missing k8s_get decision: %s", summary)
	}
	if !strings.Contains(summary, "powershell") || !strings.Contains(summary, "decision=BLOCK") {
		t.Fatalf("summary missing powershell decision: %s", summary)
	}
	if !strings.Contains(summary, "100 tokens entrada") {
		t.Fatalf("summary missing token usage: %s", summary)
	}
}

func TestResumeContextRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	for _, malicious := range []string{"../secrets", "sess/../../etc/passwd", "a/b", "."} {
		if _, err := ResumeContext(malicious); err == nil {
			t.Fatalf("session-id %q: expected rejection, got nil error", malicious)
		}
	}
}

func TestResumeContextMissingSessionIsAHelpfulError(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	_, err := ResumeContext("does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "abriendo sesion") {
		t.Fatalf("err = %v, want an open-error mentioning the session", err)
	}
}

func TestResumeContextIgnoresMalformedFinalLine(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	writeSessionLog(t, dir, "sess-2", strings.Join([]string{
		`{"Time":"2026-08-15T10:00:00Z","SessionID":"sess-2","Tool":"k8s_get","Engine":"policy","Decision":"ALLOW","RiskScore":1,"Executed":true,"Outcome":"completed"}`,
		`{"Tool":"powershell","Decision":`, // truncated / partial final line
	}, "\n"))

	summary, err := ResumeContext("sess-2")
	if err != nil {
		t.Fatalf("ResumeContext: %v", err)
	}
	if !strings.Contains(summary, "k8s_get") {
		t.Fatalf("summary should still include the valid line: %s", summary)
	}
}
