package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config is the project-local loom.config.json schema.
type Config struct {
	DefaultProvider string `json:"default_provider"`

	Providers struct {
		Anthropic struct {
			Model string `json:"model"`
		} `json:"anthropic"`
		Ollama struct {
			Model   string `json:"model"`
			BaseURL string `json:"base_url"`
		} `json:"ollama"`
	} `json:"providers"`

	CustomProviders []CustomProviderConfig `json:"custom_providers"`
	MCPServers      []MCPServerConfig      `json:"mcp_servers,omitempty"`
	Routing         []RoutingRule          `json:"routing"`
	Governance      GovernanceConfig       `json:"governance"`
	Sre             SreConfig              `json:"sre,omitempty"`
}

// MCPServerConfig defines a local stdio MCP server whose tools are made
// available to Loom alongside its built-in tools. The command is executed
// directly (without a shell), and args are passed as an argument slice.
type MCPServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// SreConfig holds defaults for Loom's native SRE tools, so an operator does
// not have to pass --context/--profile/--namespace on every single call and
// the model does not have to guess them either.
type SreConfig struct {
	// KubeContext is passed as `--context` to every native k8s_* tool call
	// when set. Empty means "whatever kubectl's current-context is".
	KubeContext string `json:"kube_context,omitempty"`

	Cloud struct {
		// Provider scopes which CLI cloud_read is allowed to invoke:
		// "aws", "gcloud", or "az". Empty means cloud_read infers it from
		// the first word of the command it's given.
		Provider string `json:"provider,omitempty"`
		// Profile/subscription passed through as --profile (aws) or
		// --subscription (az) or left for gcloud's own active config.
		Profile string `json:"profile,omitempty"`
	} `json:"cloud,omitempty"`

	Observability struct {
		PrometheusURL    string `json:"prometheus_url,omitempty"`
		GrafanaURL       string `json:"grafana_url,omitempty"`
		GrafanaTokenEnv  string `json:"grafana_token_env,omitempty"`
		DatadogSite      string `json:"datadog_site,omitempty"` // e.g. "datadoghq.com", "datadoghq.eu"
		DatadogAPIKeyEnv string `json:"datadog_api_key_env,omitempty"`
		DatadogAppKeyEnv string `json:"datadog_app_key_env,omitempty"`
	} `json:"observability,omitempty"`

	// RunbooksDir is where `loom run --runbook <name>` looks for
	// <name>.json. Defaults to "./runbooks" when empty.
	RunbooksDir string `json:"runbooks_dir,omitempty"`

	// Environments maps the identifiers an operator actually uses (kube
	// contexts, cloud profiles, Terraform workspaces, repo paths) to an
	// environment tier. It is the declared source of truth for "is this
	// production?"; the keyword heuristic is only a fallback when nothing
	// here matches.
	Environments []EnvironmentConfig `json:"environments,omitempty"`
}

// Environment tiers. Anything that cannot be resolved is TierUnknown, which
// Loom treats as production for infrastructure changes (fail-safe).
const (
	TierProd    = "prod"
	TierStaging = "staging"
	TierDev     = "dev"
	TierUnknown = "unknown"
)

// EnvironmentConfig declares one environment and every identifier that
// points at it. A tool call whose context/profile/workspace token matches
// one of these exactly (or whose path starts with one of Paths) resolves to
// this environment.
type EnvironmentConfig struct {
	Name                string   `json:"name"`
	Tier                string   `json:"tier"` // prod | staging | dev
	KubeContexts        []string `json:"kube_contexts,omitempty"`
	CloudProfiles       []string `json:"cloud_profiles,omitempty"`
	TerraformWorkspaces []string `json:"terraform_workspaces,omitempty"`
	Paths               []string `json:"paths,omitempty"`
}

// CustomProviderConfig defines any provider that implements the OpenAI Chat
// Completions protocol: hosted services, gateways, or local servers.
type CustomProviderConfig struct {
	ID                string  `json:"id"`
	BaseURL           string  `json:"base_url"`
	Model             string  `json:"model"`
	APIKeyEnv         string  `json:"api_key_env"`
	SupportsTooling   bool    `json:"supports_tooling"`
	CostInputPerMTok  float64 `json:"cost_input_per_mtok"`
	CostOutputPerMTok float64 `json:"cost_output_per_mtok"`
}

type RoutingRule struct {
	Task     string `json:"task"`
	Provider string `json:"provider"`
}

type GovernanceConfig struct {
	Enabled bool   `json:"enabled"`
	Engine  string `json:"engine"`
	BaseURL string `json:"base_url,omitempty"`
	Token   string `json:"token,omitempty"`
}

// Route is the concrete model selection made for one request.
type Route struct {
	Task     string
	Provider string
	Reason   string
}

func Default() Config {
	cfg := Config{DefaultProvider: "anthropic"}
	cfg.Providers.Anthropic.Model = "claude-sonnet-4-6"
	cfg.Providers.Ollama.Model = "qwen2.5-coder:7b"
	cfg.Providers.Ollama.BaseURL = "http://localhost:11434"
	cfg.Routing = []RoutingRule{
		{Task: "trivial_edit", Provider: "ollama"},
		{Task: "planning", Provider: "anthropic"},
		{Task: "security_review", Provider: "anthropic"},
		{Task: "incident_response", Provider: "anthropic"},
	}
	cfg.Governance = GovernanceConfig{
		Enabled: true,
		Engine:  "devmind",
		BaseURL: "https://devmind-2cej.onrender.com",
	}
	return cfg
}

func Load(path string) (Config, error) {
	if path == "" {
		path = "loom.config.json"
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ProviderFor returns the configured ID without imposing a fixed provider
// vocabulary. A routing rule can therefore name any custom provider.
func (c Config) ProviderFor(task string) string {
	for _, rule := range c.Routing {
		if rule.Task == task {
			return rule.Provider
		}
	}
	return c.DefaultProvider
}

// CustomProvider finds a custom provider by its routing ID.
func (c Config) CustomProvider(id string) (CustomProviderConfig, bool) {
	for _, provider := range c.CustomProviders {
		if provider.ID == id {
			return provider, true
		}
	}
	return CustomProviderConfig{}, false
}

func (c Config) RouteForPrompt(prompt, taskHint string) Route {
	task := strings.TrimSpace(strings.ToLower(taskHint))
	reason := "explicit task hint"
	if task == "" {
		task, reason = classify(prompt)
	}
	return Route{Task: task, Provider: c.ProviderFor(task), Reason: reason}
}

func classify(prompt string) (string, string) {
	lower := strings.ToLower(prompt)
	incidentWords := []string{
		"incidente", "incident", "outage", "caido", "caída", "caida",
		"down", "degradado", "degradada", "sev1", "sev2", "sev-1", "sev-2",
		"on-call", "oncall", "pagerduty", "alerta critica", "alerta crítica",
	}
	for _, word := range incidentWords {
		if strings.Contains(lower, word) {
			return "incident_response", fmt.Sprintf("incident keyword %q", word)
		}
	}
	securityWords := []string{
		"security", "seguridad", "permission", "permiso", "auth", "credential",
		"secret", "production", "producción", "prod", "deploy", "terraform",
		"tofu", "kubernetes", "kubectl", "helm", "database", "base de datos",
		"delete", "borrar", "migrate", "iam", "rol", "role", "rotar",
		"rotate", "terminate", "drop", "drain",
	}
	for _, word := range securityWords {
		if strings.Contains(lower, word) {
			return "security_review", fmt.Sprintf("risk keyword %q", word)
		}
	}
	planningWords := []string{
		"plan", "arquitect", "design", "diseña", "analiza", "review", "revisa",
		"runbook", "postmortem", "post-mortem", "capacidad", "capacity",
		"autoscal", "slo", "sla",
	}
	for _, word := range planningWords {
		if strings.Contains(lower, word) {
			return "planning", fmt.Sprintf("planning keyword %q", word)
		}
	}
	return "trivial_edit", "no risk or planning keywords"
}
