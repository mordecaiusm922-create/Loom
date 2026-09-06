package config

import "testing"

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
