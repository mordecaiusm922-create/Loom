package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitConfigIsReadableOnlyByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "grantline.config.json")
	if err := writeDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode = %o, want no group or other permissions", mode)
	}
}

func TestInitNeverOverwritesAnExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grantline.config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultConfig(path); !os.IsExist(err) {
		t.Fatalf("err = %v, want an already-exists error", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "{}" {
		t.Fatalf("existing config was modified: %q", data)
	}
}

func TestAgentIDFallsBackToLegacyVariable(t *testing.T) {
	t.Setenv("GRANTLINE_AGENT_ID", "")
	t.Setenv("LOOM_AGENT_ID", "ci-agent")
	if got := agentIDFromEnv(); got != "ci-agent" {
		t.Fatalf("agentIDFromEnv() = %q, want the legacy LOOM_AGENT_ID", got)
	}
	t.Setenv("GRANTLINE_AGENT_ID", "new-agent")
	if got := agentIDFromEnv(); got != "new-agent" {
		t.Fatalf("agentIDFromEnv() = %q, want GRANTLINE_AGENT_ID first", got)
	}
}

func TestInitMigratesALegacyLoomConfig(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	legacy := []byte(`{"default_provider":"groq"}`)
	if err := os.WriteFile("loom.config.json", legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	cmdInit()
	got, err := os.ReadFile("grantline.config.json")
	if err != nil || string(got) != string(legacy) {
		t.Fatalf("grantline.config.json = %q, %v; want the legacy settings", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat("grantline.config.json"); info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("mode = %o, want owner-only", info.Mode().Perm())
		}
	}
}
