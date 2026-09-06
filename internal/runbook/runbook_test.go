package runbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunbook(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(content), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
}

func TestLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeRunbook(t, dir, "pod-crashloop", `{
		"title": "Pod en CrashLoopBackOff",
		"task_hint": "incident_response",
		"steps": ["k8s_describe del pod", "k8s_logs --previous", "revisar el ultimo deploy"]
	}`)

	rb, err := Load(dir, "pod-crashloop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rb.Title != "Pod en CrashLoopBackOff" {
		t.Fatalf("Title = %q", rb.Title)
	}
	if rb.TaskHint != "incident_response" {
		t.Fatalf("TaskHint = %q", rb.TaskHint)
	}
	if len(rb.Steps) != 3 {
		t.Fatalf("Steps = %d, want 3", len(rb.Steps))
	}
}

func TestLoadMissingRunbookIsAHelpfulError(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "no encontrado") {
		t.Fatalf("err = %v, want a not-found error mentioning the runbook", err)
	}
}

func TestLoadRejectsRunbookWithoutSteps(t *testing.T) {
	dir := t.TempDir()
	writeRunbook(t, dir, "empty", `{"title": "vacio"}`)
	_, err := Load(dir, "empty")
	if err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("err = %v, want a missing-steps error", err)
	}
}

func TestListReturnsSortedNamesWithoutExtension(t *testing.T) {
	dir := t.TempDir()
	writeRunbook(t, dir, "zzz-last", `{"title":"z","steps":["a"]}`)
	writeRunbook(t, dir, "aaa-first", `{"title":"a","steps":["a"]}`)

	names, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "aaa-first" || names[1] != "zzz-last" {
		t.Fatalf("names = %v, want [aaa-first zzz-last]", names)
	}
}

func TestListOnMissingDirReturnsEmptyNotError(t *testing.T) {
	names, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("List: %v, want nil error for a missing directory", err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %v, want empty", names)
	}
}

func TestComposePromptIncludesStepsAndExtraContext(t *testing.T) {
	rb := Runbook{Title: "Alta latencia en checkout", Steps: []string{"revisar dashboards", "revisar deploys recientes"}}
	prompt := rb.ComposePrompt("p99 subio a 4s hace 10 minutos")
	if !strings.Contains(prompt, "Alta latencia en checkout") {
		t.Fatalf("prompt missing title: %s", prompt)
	}
	if !strings.Contains(prompt, "1. revisar dashboards") {
		t.Fatalf("prompt missing numbered step: %s", prompt)
	}
	if !strings.Contains(prompt, "p99 subio a 4s hace 10 minutos") {
		t.Fatalf("prompt missing extra context: %s", prompt)
	}
}
