package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"loom/internal/config"
	"loom/internal/governance"
)

// TestShellConfigRegistersExactlyOneShell locks in that sre.shell swaps the
// shell tool instead of adding a second one: offering both would let the
// model write bash syntax into PowerShell (or the reverse).
func TestShellConfigRegistersExactlyOneShell(t *testing.T) {
	cases := []struct {
		shell, want, notWant string
	}{
		{"", "powershell", "bash"},
		{"powershell", "powershell", "bash"},
		{"bash", "bash", "powershell"},
		{"BASH", "bash", "powershell"},
	}
	for _, tc := range cases {
		registry := NewRegistry(&fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}, "agent", "session", config.SreConfig{Shell: tc.shell})
		got := map[string]bool{}
		for _, tool := range registry.List() {
			got[tool.Name] = true
		}
		if !got[tc.want] || got[tc.notWant] {
			t.Fatalf("shell=%q: registered %v, want %q and not %q", tc.shell, got, tc.want, tc.notWant)
		}
	}
}

// TestBashInfraCommandUsesInfraGovernance is the bash counterpart of the
// powershell classification: an infra change through bash must reach
// EvaluateChange with prod blast radius, not the generic action path.
func TestBashInfraCommandUsesInfraGovernance(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block, Why: []string{"test"}}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{Shell: "bash"})

	_, err := registry.Execute(context.Background(), "bash", map[string]any{"command": "terraform destroy -auto-approve"}, true)
	if err == nil || !strings.Contains(err.Error(), "BLOQUEADO") {
		t.Fatalf("err = %v, want a DevMind block", err)
	}
	if engine.changeCalls != 1 || engine.actionCalls != 0 {
		t.Fatalf("changeCalls=%d actionCalls=%d, want 1/0", engine.changeCalls, engine.actionCalls)
	}
	if engine.lastBlast != "org" {
		t.Fatalf("blast radius = %q, want org for a prod destroy", engine.lastBlast)
	}
}

func TestBashToolRunsCommand(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{Shell: "bash"})

	out, err := registry.Execute(context.Background(), "bash", map[string]any{"command": "echo loom-$((40+2))"}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "loom-42") {
		t.Fatalf("output = %q, want bash arithmetic expansion", out)
	}
}

// TestBashPipefailReportsFailure: without pipefail, `false | cat` exits 0
// and a failed command inside a pipeline would be reported as success.
func TestBashPipefailReportsFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{Shell: "bash"})

	if _, err := registry.Execute(context.Background(), "bash", map[string]any{"command": "false | cat"}, false); err == nil {
		t.Fatal("want an error from a failing pipeline")
	}
}

// TestPickWindowsBashSkipsWSLLauncher is the regression test for Windows
// machines where System32\bash.exe (the WSL launcher) comes first on PATH
// and fails with "no distributions installed": Git Bash must win.
func TestPickWindowsBashSkipsWSLLauncher(t *testing.T) {
	existing := map[string]bool{
		`C:\Windows\System32\bash.exe`:      true,
		`C:\Program Files\Git\bin\bash.exe`: true,
	}
	exists := func(p string) bool { return existing[p] }
	got := pickWindowsBash([]string{`C:\Windows\System32\bash.exe`, `C:\Program Files\Git\bin\bash.exe`}, `C:\Windows`, exists)
	if got != `C:\Program Files\Git\bin\bash.exe` {
		t.Fatalf("got %q, want Git Bash", got)
	}
	// Case and an unset SystemRoot must not let the launcher through.
	if got := pickWindowsBash([]string{`c:\WINDOWS\system32\BASH.EXE`}, "", func(string) bool { return true }); got != "" {
		t.Fatalf("got %q, want the WSL launcher skipped", got)
	}
	if got := pickWindowsBash([]string{`C:\nope\bash.exe`}, `C:\Windows`, exists); got != "" {
		t.Fatalf("got %q for a missing file", got)
	}
}
