package config

import (
	"os"
	"runtime"
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

// TestShellNameDefaultsToPowerShell covers a zero SreConfig built in code;
// the per-OS default for real configs is applied by Load/Default.
func TestShellNameDefaultsToPowerShell(t *testing.T) {
	if got := (SreConfig{}).ShellName(); got != ShellPowerShell {
		t.Fatalf("ShellName() = %q, want %q", got, ShellPowerShell)
	}
	if got := (SreConfig{Shell: " Bash "}).ShellName(); got != ShellBash {
		t.Fatalf("ShellName() = %q, want %q", got, ShellBash)
	}
}

func TestDefaultShellPerOS(t *testing.T) {
	cases := map[string]string{"windows": ShellPowerShell, "linux": ShellBash, "darwin": ShellBash}
	for goos, want := range cases {
		if got := DefaultShell(goos); got != want {
			t.Fatalf("DefaultShell(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestLoadAppliesHostShellDefault(t *testing.T) {
	want := DefaultShell(runtime.GOOS)
	dir := t.TempDir()

	missing, err := Load(dir + "/absent.json")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Sre.Shell != want {
		t.Fatalf("no file: shell = %q, want %q", missing.Sre.Shell, want)
	}

	path := dir + "/loom.config.json"
	if err := os.WriteFile(path, []byte(`{"sre":{"kube_context":"prod"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unset, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if unset.Sre.Shell != want || unset.Sre.KubeContext != "prod" {
		t.Fatalf("file without shell: got shell=%q context=%q", unset.Sre.Shell, unset.Sre.KubeContext)
	}

	if err := os.WriteFile(path, []byte(`{"sre":{"shell":"powershell"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Sre.ShellName() != ShellPowerShell {
		t.Fatalf("explicit powershell was overridden: %q", explicit.Sre.Shell)
	}
}

func TestDevMindTokenFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/loom.config.json"
	if err := os.WriteFile(path, []byte(`{"governance":{"enabled":true,"engine":"devmind","token":"dvm_from_file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(DevMindTokenEnv, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Governance.Token != "dvm_from_file" {
		t.Fatalf("empty env must keep the file token, got %q", cfg.Governance.Token)
	}

	t.Setenv(DevMindTokenEnv, "  dvm_from_env  ")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Governance.Token != "dvm_from_env" {
		t.Fatalf("env must override the file token, got %q", cfg.Governance.Token)
	}

	noFile, err := Load(dir + "/absent.json")
	if err != nil {
		t.Fatal(err)
	}
	if noFile.Governance.Token != "dvm_from_env" {
		t.Fatalf("env must apply with no config file, got %q", noFile.Governance.Token)
	}
}
