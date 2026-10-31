package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mordecaiusm922-create/grantline/internal/agent"
	"github.com/mordecaiusm922-create/grantline/internal/config"
	"github.com/mordecaiusm922-create/grantline/internal/governance"
	"github.com/mordecaiusm922-create/grantline/internal/runbook"
	"github.com/mordecaiusm922-create/grantline/internal/tui"
)

// version is set at release time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Grantline -- the agent harness for SRE, DevOps, Platform Engineering and Cloud Security.

Usage:
  grantline run [--task type] [--runbook name] [--resume session] [--dry-run] [--non-interactive] "<prompt>"  Run a governed task
  grantline chat [--dry-run]              Interactive TUI on the same session
  grantline runbooks                      List the available runbooks
  grantline init                          Create grantline.config.json (never overwrites)
  grantline doctor                        Check the local configuration
  grantline config                        Print the effective configuration
  grantline version                       Print the version

Task types: trivial_edit, planning, security_review, incident_response.
Domain: Terraform/OpenTofu, Kubernetes/Helm, cloud CLIs (aws/gcloud/az),
CI/CD, observability (Prometheus/Grafana/Datadog), incident management
and access control (IAM, secret rotation). The underlying shell is
bash on Linux/macOS and PowerShell on Windows (change it with "sre.shell");
there are also read-only native tools
(k8s_get, k8s_describe, k8s_logs, cloud_read, metrics_query).
Governance is mandatory: export DEVMIND_TOKEN=dvm_... and verify with
grantline doctor. A local opt-out requires setting
GRANTLINE_UNSAFE_DISABLE_GOVERNANCE=1 explicitly.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "chat":
		cmdChat(os.Args[2:])
	case "runbooks":
		cmdRunbooks()
	case "init":
		cmdInit()
	case "doctor":
		cmdDoctor()
	case "config":
		cmdConfig()
	case "version":
		fmt.Println("grantline " + version)
	default:
		fmt.Print(usage)
		os.Exit(1)
	}
}

func cmdRun(args []string) {
	taskHint := ""
	runbookName := ""
	resumeID := ""
	dryRun := false
	nonInteractive := false
	promptParts := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--task":
			if i+1 == len(args) {
				fatal("--task requires a value")
			}
			taskHint = args[i+1]
			i++
		case "--runbook":
			if i+1 == len(args) {
				fatal("--runbook requires a value")
			}
			runbookName = args[i+1]
			i++
		case "--resume":
			if i+1 == len(args) {
				fatal("--resume requires a session id")
			}
			resumeID = args[i+1]
			i++
		case "--dry-run":
			dryRun = true
		case "--non-interactive":
			nonInteractive = true
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	cfg, err := config.Load("")
	if err != nil {
		fatal("loading config: %v", err)
	}

	prompt := strings.Join(promptParts, " ")
	if runbookName != "" {
		rb, err := runbook.Load(cfg.Sre.RunbooksDir, runbookName)
		if err != nil {
			fatal("%v", err)
		}
		prompt = rb.ComposePrompt(prompt)
		if taskHint == "" {
			taskHint = rb.TaskHint
		}
	}
	if strings.TrimSpace(prompt) == "" {
		fatal("usage: grantline run [--task type] [--runbook name] [--resume session] [--dry-run] [--non-interactive] \"<prompt>\"")
	}
	if resumeID != "" {
		contextSummary, err := agent.ResumeContext(resumeID)
		if err != nil {
			fatal("resuming session: %v", err)
		}
		prompt = contextSummary + "\n\nNew context from the operator:\n" + prompt
	}

	agentID := agentIDFromEnv()
	sessionID := agent.NewSessionID()
	session, err := agent.NewSession(cfg, agentID, sessionID)
	if err != nil {
		fatal("starting session: %v", err)
	}
	defer session.Close()
	session.SetDryRun(dryRun)
	session.SetNonInteractive(nonInteractive)
	if err := session.RunWithTask(context.Background(), prompt, taskHint); err != nil {
		fatal("%v", err)
	}
}

func cmdRunbooks() {
	cfg, err := config.Load("")
	if err != nil {
		fatal("loading config: %v", err)
	}
	dir := cfg.Sre.RunbooksDir
	if dir == "" {
		dir = runbook.DefaultDir
	}
	names, err := runbook.List(dir)
	if err != nil {
		fatal("listing runbooks in %s: %v", dir, err)
	}
	if len(names) == 0 {
		fmt.Printf("no runbooks in %s\n", dir)
		return
	}
	fmt.Printf("runbooks available in %s:\n", dir)
	for _, name := range names {
		rb, err := runbook.Load(dir, name)
		if err != nil {
			fmt.Printf("  %s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %-20s %s\n", name, rb.Title)
	}
}

func cmdChat(args []string) {
	dryRun := false
	for _, arg := range args {
		if arg != "--dry-run" {
			fatal("usage: grantline chat [--dry-run]")
		}
		dryRun = true
	}
	cfg, err := config.Load("")
	if err != nil {
		fatal("loading config: %v", err)
	}
	agentID := agentIDFromEnv()
	sessionID := agent.NewSessionID()
	if err := tui.Run(cfg, agentID, sessionID, dryRun); err != nil {
		fatal("%v", err)
	}
}

func cmdInit() {
	path := config.ConfigFile
	if legacy, err := os.ReadFile(config.LegacyConfigFile); err == nil {
		// Keep the settings of a project set up before the rename from Loom.
		if err := writeConfig(path, legacy); err != nil {
			if os.IsExist(err) {
				fatal("%s already exists; not overwritten", path)
			}
			fatal("creating config: %v", err)
		}
		fmt.Printf("created %s from %s; delete %s once you have checked it\n", path, config.LegacyConfigFile, config.LegacyConfigFile)
		return
	}
	if err := writeDefaultConfig(path); err != nil {
		if os.IsExist(err) {
			fatal("%s already exists; not overwritten", path)
		}
		fatal("creating config: %v", err)
	}
	fmt.Printf("created %s\n", path)
}

// configFileMode keeps grantline.config.json readable only by its owner: it may
// hold a DevMind token (prefer DEVMIND_TOKEN) or provider settings.
const configFileMode = 0o600

// writeDefaultConfig creates path with the default configuration and never
// overwrites an existing file.
func writeDefaultConfig(path string) error {
	data, err := json.MarshalIndent(config.Default(), "", "  ")
	if err != nil {
		return err
	}
	return writeConfig(path, append(data, '\n'))
}

// writeConfig creates path with owner-only permissions and never overwrites
// an existing file.
func writeConfig(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, configFileMode)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func cmdDoctor() {
	cfg, err := config.Load("")
	if err != nil {
		fatal("invalid config: %v", err)
	}
	failures := 0
	fmt.Println("Grantline doctor")
	fmt.Println("  config: OK")
	switch {
	case cfg.Governance.Enabled && cfg.Governance.ResolvedEngine() == config.EngineDevMind:
		fmt.Printf("  governance: DevMind (%s)\n", cfg.Governance.BaseURL)
		failures += checkDevMind(cfg)
	case cfg.Governance.Enabled:
		fmt.Println("  governance: OK -- local policy (reads run, changes ask you first, catastrophic commands are blocked)")
		fmt.Println("  devmind: not connected -- set DEVMIND_TOKEN for fine-grained verdicts, team approvals and an audit trail")
	case os.Getenv("GRANTLINE_UNSAFE_DISABLE_GOVERNANCE") == "1":
		fmt.Println("  governance: UNSAFE opt-out enabled")
	default:
		fmt.Println("  governance: ERROR (governance.enabled must be true)")
		failures++
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Println("  anthropic: API key missing (required only when routed to Anthropic)")
	} else {
		fmt.Println("  anthropic: API key configured")
	}
	fmt.Printf("  ollama: configured at %s (connectivity is checked when used)\n", cfg.Providers.Ollama.BaseURL)
	if cfg.Sre.ShellName() == config.ShellBash {
		if _, err := exec.LookPath("bash"); err == nil {
			fmt.Println("  shell: bash found (sre.shell=bash)")
		} else {
			fmt.Println("  shell: ERROR -- sre.shell=bash but bash not found in PATH")
			failures++
		}
	} else if _, err := exec.LookPath("pwsh"); err == nil {
		fmt.Println("  shell: pwsh (PowerShell 7+) found")
	} else if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			fmt.Println("  shell: powershell.exe found (pwsh not found -- consider installing PowerShell 7+)")
		} else {
			fmt.Println("  shell: ERROR -- neither pwsh nor powershell.exe found in PATH")
			failures++
		}
	} else {
		fmt.Println("  shell: ERROR -- sre.shell=powershell but pwsh not found in PATH")
		failures++
	}
	kubeOK := checkBinary("kubectl")
	terraformOK := checkBinary("terraform") || checkBinary("tofu")
	fmt.Printf("  kubectl: %s\n", presence(kubeOK))
	fmt.Printf("  terraform/tofu: %s\n", presence(terraformOK))
	runbookNames, _ := runbook.List(cfg.Sre.RunbooksDir)
	fmt.Printf("  runbooks: %d available\n", len(runbookNames))
	if failures > 0 {
		os.Exit(1)
	}
}

func cmdConfig() {
	cfg, err := config.Load("")
	if err != nil {
		fatal("loading config: %v", err)
	}
	if cfg.Governance.Token != "" {
		cfg.Governance.Token = "REDACTED"
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(data))
}

// agentIDFromEnv is the agent identity Grantline presents to DevMind.
func agentIDFromEnv() string {
	if id := os.Getenv("GRANTLINE_AGENT_ID"); id != "" {
		return id
	}
	if id := os.Getenv("LOOM_AGENT_ID"); id != "" { // name before the rename
		return id
	}
	return "grantline-sre"
}

// checkDevMind reports where the token comes from and proves it works with
// one live probe, so a missing or rejected token surfaces here instead of
// as an unexplained REVIEW on every tool call. Returns the failure count.
func checkDevMind(cfg config.Config) int {
	switch {
	case strings.TrimSpace(os.Getenv(config.DevMindTokenEnv)) != "":
		fmt.Println("  devmind token: set via DEVMIND_TOKEN")
	case cfg.Governance.Token != "":
		fmt.Println("  devmind token: set in grantline.config.json (prefer DEVMIND_TOKEN so it never sits in a file)")
	default:
		fmt.Println("  devmind token: ERROR -- missing; export DEVMIND_TOKEN=dvm_...")
		return 1
	}
	fmt.Println("  devmind: checking connectivity and token (up to a minute if the service was idle)...")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine := governance.NewDevMindEngine(cfg.Governance.BaseURL, cfg.Governance.Token)
	if err := engine.Check(ctx, agentIDFromEnv()); err != nil {
		fmt.Printf("  devmind: ERROR -- %v\n", err)
		return 1
	}
	fmt.Println("  devmind: OK (token accepted)")
	return 0
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func checkBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func presence(ok bool) string {
	if ok {
		return "OK"
	}
	return "not found in PATH"
}
