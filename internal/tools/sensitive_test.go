package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
	"github.com/mordecaiusm922-create/loom/internal/redact"
)

func TestSensitivePathsAreRefused(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	refused := []string{
		filepath.Join(home, ".aws", "credentials"),
		"~/.aws/credentials",
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".kube", "config"),
		filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"),
		"infra/.env",
		"infra/.env.production",
		"certs/server.key",
		"certs/tls.pem",
		"envs/prod/terraform.tfstate",
		"terraform.tfstate.backup",
		filepath.Join(home, "project", "..", ".aws", "credentials"),
	}
	for _, path := range refused {
		if sensitivePathReason(path) == "" {
			t.Errorf("%s: allowed, want refused", path)
		}
	}
}

func TestOrdinaryInfraFilesAreAllowed(t *testing.T) {
	allowed := []string{
		"infra/main.tf",
		"k8s/deployment.yaml",
		"charts/api/values-prod.yaml",
		".env.example",
		"runbooks/pod-crashloop.json",
		"README.md",
	}
	for _, path := range allowed {
		if reason := sensitivePathReason(path); reason != "" {
			t.Errorf("%s: refused (%s), want allowed", path, reason)
		}
	}
}

// A symlink with an innocent name must not launder a credential file.
func TestSensitivePathFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "server.pem")
	if err := os.WriteFile(target, []byte("-----BEGIN PRIVATE KEY-----"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "notes.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if sensitivePathReason(link) == "" {
		t.Fatal("symlink to a .pem was allowed")
	}
}

func TestReadFileRefusesCredentialsAndNeverReturnsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("DB_PASSWORD=supersecretvalue"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	out, err := registry.Execute(context.Background(), "read_file", map[string]any{"path": path}, false)
	if err == nil || !strings.Contains(err.Error(), "ruta protegida") {
		t.Fatalf("err = %v, want protected-path refusal", err)
	}
	if strings.Contains(out, "supersecretvalue") {
		t.Fatal("credential content returned to the model")
	}
}

func TestWriteFileRefusesCredentialPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.key")
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	if _, err := registry.Execute(context.Background(), "write_file", map[string]any{"path": path, "content": "x"}, false); err == nil {
		t.Fatal("write_file to a .key path succeeded")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("file was written")
	}
}

// Every tool output is redacted in Registry.Execute, so a tool that does
// not know about secrets (an MCP server, a shell command) cannot leak one
// into the conversation.
func TestExecuteRedactsEveryToolOutput(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	if err := registry.Register(Tool{
		Name: "leaky",
		Run: func(context.Context, map[string]any) (string, error) {
			return "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := registry.Execute(context.Background(), "leaky", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(out, redact.Mask) {
		t.Fatalf("out = %q, want the key masked", out)
	}
}
