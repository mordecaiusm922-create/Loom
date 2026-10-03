package config

import (
	"os"
	"testing"
)

func TestRouteForPromptPrioritizesRisk(t *testing.T) {
	cfg := Default()
	route := cfg.RouteForPrompt("borra la base de datos de produccion", "")
	if route.Task != "security_review" || route.Provider != "anthropic" {
		t.Fatalf("route = %#v, want security_review through anthropic", route)
	}
}

func TestRouteForPromptAcceptsExplicitTask(t *testing.T) {
	cfg := Default()
	route := cfg.RouteForPrompt("haz un plan de despliegue", "trivial_edit")
	if route.Task != "trivial_edit" || route.Provider != "ollama" {
		t.Fatalf("route = %#v, want explicit local route", route)
	}
}

func TestRouteForPromptAcceptsCustomProviderID(t *testing.T) {
	cfg := Default()
	cfg.CustomProviders = []CustomProviderConfig{{ID: "groq", BaseURL: "https://api.groq.com/openai/v1"}}
	cfg.Routing = []RoutingRule{{Task: "planning", Provider: "groq"}}
	route := cfg.RouteForPrompt("haz un plan", "")
	if route.Provider != "groq" {
		t.Fatalf("route = %#v, want groq", route)
	}
	if _, ok := cfg.CustomProvider(route.Provider); !ok {
		t.Fatal("custom provider was not resolved")
	}
}

func TestLoadRejectsUnknownShell(t *testing.T) {
	path := t.TempDir() + "/loom.config.json"
	if err := os.WriteFile(path, []byte(`{"sre":{"shell":"zsh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want an error for sre.shell=zsh")
	}
}

func TestShellNameDefaultsToPowerShell(t *testing.T) {
	if got := (SreConfig{}).ShellName(); got != ShellPowerShell {
		t.Fatalf("ShellName() = %q, want %q", got, ShellPowerShell)
	}
	if got := (SreConfig{Shell: " Bash "}).ShellName(); got != ShellBash {
		t.Fatalf("ShellName() = %q, want %q", got, ShellBash)
	}
}
