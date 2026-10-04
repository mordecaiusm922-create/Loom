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

	"github.com/mordecaiusm922-create/loom/internal/agent"
	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
	"github.com/mordecaiusm922-create/loom/internal/runbook"
	"github.com/mordecaiusm922-create/loom/internal/tui"
)

const usage = `Loom -- runtime gobernado para agentes de SRE / Platform Engineering.

Uso:
  loom run [--task tipo] [--runbook nombre] [--resume sesion] [--dry-run] [--non-interactive] "<prompt>"  Ejecuta una tarea gobernada
  loom chat [--dry-run]              TUI interactiva sobre la misma sesion
  loom runbooks                      Lista los runbooks disponibles
  loom init                         Crea loom.config.json sin sobrescribir
  loom doctor                       Comprueba la configuracion local
  loom config                       Imprime la configuracion efectiva
  loom version                      Imprime la version

Tipos de tarea: trivial_edit, planning, security_review, incident_response.
Dominio: Terraform/OpenTofu, Kubernetes/Helm, CLIs de cloud (aws/gcloud/az),
CI/CD, observabilidad (Prometheus/Grafana/Datadog), gestion de incidentes
y control de acceso (IAM, rotacion de secretos). El shell subyacente es
bash en Linux/macOS y PowerShell en Windows (cambialo con "sre.shell");
ademas hay herramientas nativas de solo lectura
(k8s_get, k8s_describe, k8s_logs, cloud_read, metrics_query).
La gobernanza es obligatoria: exporta DEVMIND_TOKEN=dvm_... y verifica con
loom doctor. Un opt-out local requiere LOOM_UNSAFE_DISABLE_GOVERNANCE=1 de
forma explicita.
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
		fmt.Println("loom v0.4.0-sre")
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
				fatal("--task requiere un valor")
			}
			taskHint = args[i+1]
			i++
		case "--runbook":
			if i+1 == len(args) {
				fatal("--runbook requiere un valor")
			}
			runbookName = args[i+1]
			i++
		case "--resume":
			if i+1 == len(args) {
				fatal("--resume requiere un session-id")
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
		fatal("cargando config: %v", err)
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
		fatal("uso: loom run [--task tipo] [--runbook nombre] [--resume sesion] [--dry-run] [--non-interactive] \"<prompt>\"")
	}
	if resumeID != "" {
		contextSummary, err := agent.ResumeContext(resumeID)
		if err != nil {
			fatal("retomando sesion: %v", err)
		}
		prompt = contextSummary + "\n\nNuevo contexto del operador:\n" + prompt
	}

	agentID := agentIDFromEnv()
	sessionID := agent.NewSessionID()
	session, err := agent.NewSession(cfg, agentID, sessionID)
	if err != nil {
		fatal("iniciando sesion: %v", err)
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
		fatal("cargando config: %v", err)
	}
	dir := cfg.Sre.RunbooksDir
	if dir == "" {
		dir = runbook.DefaultDir
	}
	names, err := runbook.List(dir)
	if err != nil {
		fatal("listando runbooks en %s: %v", dir, err)
	}
	if len(names) == 0 {
		fmt.Printf("no hay runbooks en %s\n", dir)
		return
	}
	fmt.Printf("runbooks disponibles en %s:\n", dir)
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
			fatal("uso: loom chat [--dry-run]")
		}
		dryRun = true
	}
	cfg, err := config.Load("")
	if err != nil {
		fatal("cargando config: %v", err)
	}
	agentID := agentIDFromEnv()
	sessionID := agent.NewSessionID()
	if err := tui.Run(cfg, agentID, sessionID, dryRun); err != nil {
		fatal("%v", err)
	}
}

func cmdInit() {
	path := "loom.config.json"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			fatal("%s ya existe; no se sobrescribio", path)
		}
		fatal("creando config: %v", err)
	}
	defer file.Close()
	data, err := json.MarshalIndent(config.Default(), "", "  ")
	if err != nil {
		fatal("serializando config: %v", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		fatal("escribiendo config: %v", err)
	}
	fmt.Printf("creado %s\n", path)
}

func cmdDoctor() {
	cfg, err := config.Load("")
	if err != nil {
		fatal("config invalida: %v", err)
	}
	failures := 0
	fmt.Println("Loom doctor")
	fmt.Println("  config: OK")
	if cfg.Governance.Enabled && cfg.Governance.Engine == "devmind" && cfg.Governance.BaseURL != "" {
		fmt.Printf("  governance: OK (%s)\n", cfg.Governance.BaseURL)
		failures += checkDevMind(cfg)
	} else if os.Getenv("LOOM_UNSAFE_DISABLE_GOVERNANCE") == "1" {
		fmt.Println("  governance: UNSAFE opt-out enabled")
	} else {
		fmt.Println("  governance: ERROR (must be enabled with engine=devmind)")
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
	fmt.Printf("  runbooks: %d disponibles\n", len(runbookNames))
	if failures > 0 {
		os.Exit(1)
	}
}

func cmdConfig() {
	cfg, err := config.Load("")
	if err != nil {
		fatal("cargando config: %v", err)
	}
	if cfg.Governance.Token != "" {
		cfg.Governance.Token = "REDACTED"
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(data))
}

// agentIDFromEnv is the agent identity Loom presents to DevMind.
func agentIDFromEnv() string {
	if id := os.Getenv("LOOM_AGENT_ID"); id != "" {
		return id
	}
	return "loom-sre"
}

// checkDevMind reports where the token comes from and proves it works with
// one live probe, so a missing or rejected token surfaces here instead of
// as an unexplained REVIEW on every tool call. Returns the failure count.
func checkDevMind(cfg config.Config) int {
	switch {
	case strings.TrimSpace(os.Getenv(config.DevMindTokenEnv)) != "":
		fmt.Println("  devmind token: set via DEVMIND_TOKEN")
	case cfg.Governance.Token != "":
		fmt.Println("  devmind token: set in loom.config.json (prefer DEVMIND_TOKEN so it never sits in a file)")
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
