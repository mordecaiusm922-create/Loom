package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
	"github.com/mordecaiusm922-create/loom/internal/redact"
)

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Run         func(ctx context.Context, input map[string]any) (string, error)
	// IsMutating tells dry-run whether this invocation must be skipped. Nil
	// denotes a read-only tool.
	IsMutating func(input map[string]any) bool
}

// AuditEvent intentionally excludes the action payload: traces must be useful
// without leaking code, credentials, or shell arguments to local logs.
type AuditEvent struct {
	Time      time.Time
	SessionID string
	Tool      string
	Engine    string // "policy" (/evaluate) o "infra" (/evaluate-change)
	Decision  governance.Decision
	RiskScore float64
	Executed  bool
	Outcome   string
	// Environment is the resolved tier (prod/staging/dev/unknown) the call
	// targeted -- see ResolveEnvironment.
	Environment string `json:",omitempty"`
	// AuditID links this local record to DevMind's audit record for the
	// same decision.
	AuditID string `json:",omitempty"`
}

type Registry struct {
	tools          map[string]Tool
	gov            governance.Engine
	agentID        string
	sessionID      string
	confirm        func(governance.Decision, *governance.EvaluateResponse) bool
	onEvent        func(AuditEvent)
	dryRun         bool
	nonInteractive bool
	sre            config.SreConfig
}

func NewRegistry(gov governance.Engine, agentID, sessionID string, sre config.SreConfig) *Registry {
	r := &Registry{tools: map[string]Tool{}, gov: gov, agentID: agentID, sessionID: sessionID, sre: sre}
	r.confirm = confirmInTerminal
	r.register(shellTool(sre.ShellName()))
	r.register(readFileTool())
	r.register(writeFileTool())
	r.register(updatePlanTool())
	r.register(k8sGetTool(sre))
	r.register(k8sDescribeTool(sre))
	r.register(k8sLogsTool(sre))
	r.register(cloudReadTool(sre))
	r.register(metricsQueryTool(sre))
	return r
}

func (r *Registry) SetObserver(observer func(AuditEvent)) { r.onEvent = observer }

func (r *Registry) SetDryRun(dryRun bool) { r.dryRun = dryRun }

// SetNonInteractive makes REVIEW and ESCALATE fail closed instead of reading
// from stdin. It is intended for CI, where no human can answer the prompt.
func (r *Registry) SetNonInteractive(nonInteractive bool) { r.nonInteractive = nonInteractive }

// SetConfirmation is intended for an embedding application or tests. The CLI
// keeps the safe default: a human must type exactly y for REVIEW/ESCALATE.
func (r *Registry) SetConfirmation(confirm func(governance.Decision, *governance.EvaluateResponse) bool) {
	r.confirm = confirm
}

func (r *Registry) register(tool Tool) { r.tools[tool.Name] = tool }

// Register adds an integration-provided tool to the normal governed
// registry. It refuses collisions so an MCP server cannot replace a native
// tool with the same name.
func (r *Registry) Register(tool Tool) error {
	if tool.Name == "" || tool.Run == nil {
		return fmt.Errorf("invalid external tool")
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q is already registered", tool.Name)
	}
	r.register(tool)
	return nil
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, tool)
	}
	return out
}

// isDestructiveCommand looks at the raw command text for destroy/delete
// intent. This is a heuristic, same caveat as DevMind's own README states
// about pattern-based detection -- it can be wrong in either direction.
func isDestructiveCommand(command string) bool {
	lower := strings.ToLower(command)
	for _, keyword := range []string{
		"destroy", "delete", "uninstall", "drop ", "terminate",
		"purge", "deallocate", "drain", "de-provision", "deprovision",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// Execute always evaluates before running, including read-only tools. A tool
// implementation cannot bypass the governance engine through this registry.
// The one exception is --dry-run: a mutating call is short-circuited before
// governance is even consulted, so a rehearsal never depends on (or spends
// quota against) DevMind being reachable. Read-only tools still run for
// real in dry-run mode -- otherwise the model couldn't investigate at all
// while rehearsing.
//
// declaredProduction is an explicit operator/caller assertion that the call
// targets production; it can only raise the resolved environment, never
// lower it.
func (r *Registry) Execute(ctx context.Context, name string, input map[string]any, declaredProduction bool) (string, error) {
	tool, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	env := ResolveEnvironment(name, input, r.sre)
	if r.dryRun && tool.IsMutating != nil && tool.IsMutating(input) {
		event := AuditEvent{Time: time.Now().UTC(), SessionID: r.sessionID, Tool: name, Engine: "dry-run", Outcome: "dry_run_skipped", Environment: env.Tier}
		r.emit(event)
		return fmt.Sprintf("[dry-run] %s was not run (mutating action); governance was not consulted", name), nil
	}

	affectsProduction := declaredProduction || env.Tier == config.TierProd
	var decision *governance.EvaluateResponse
	var err error
	engineUsed := "policy"
	if isShellTool(name) {
		if command, _ := input["command"].(string); command != "" {
			if changeType, isChange := iacChangeType(command); isChange {
				// Fail-safe: an infrastructure change whose target cannot be
				// resolved is evaluated as production. Reads are not, so an
				// unmapped cluster does not turn every `kubectl get` into a
				// REVIEW.
				if env.Tier == config.TierUnknown {
					affectsProduction = true
				}
				blastRadius := ""
				if affectsProduction && isDestructiveCommand(command) {
					blastRadius = "org"
				}
				decision, err = r.gov.EvaluateChange(ctx, r.agentID, changeType, "infrastructure", command, affectsProduction, blastRadius)
				engineUsed = "infra"
			}
		}
	}
	if decision == nil && err == nil {
		decision, err = r.gov.EvaluateAction(ctx, r.agentID, name, "execute", fmt.Sprintf("%v", input), affectsProduction)
	}
	if err != nil {
		return "", fmt.Errorf("governance check failed: %w", err)
	}
	event := AuditEvent{Time: time.Now().UTC(), SessionID: r.sessionID, Tool: name, Engine: engineUsed, Decision: decision.DecisionValue, RiskScore: decision.RiskScore, Environment: env.Tier, AuditID: decision.AuditID}
	switch decision.DecisionValue {
	case governance.Block:
		event.Outcome = "blocked"
		r.emit(event)
		return "", fmt.Errorf("BLOCKED by %s: %s", governance.EngineName(r.gov), strings.Join(decision.Why, "; "))
	case governance.Escalate, governance.Review:
		if r.nonInteractive {
			event.Outcome = "human_review_required"
			r.emit(event)
			return "", fmt.Errorf("action requires human review (%s) and could not be confirmed in a non-interactive environment", decision.DecisionValue)
		}
		if !r.confirm(decision.DecisionValue, decision) {
			event.Outcome = "cancelled"
			r.emit(event)
			return "", fmt.Errorf("action cancelled by the operator after a %s warning", decision.DecisionValue)
		}
	case governance.Rewrite:
		event.Outcome = "rewrite_required"
		r.emit(event)
		return "", fmt.Errorf("%s requires this action to be rewritten; Loom does not apply REWRITE automatically", governance.EngineName(r.gov))
	case governance.Allow:
		// continue
	default:
		event.Outcome = "invalid_decision"
		r.emit(event)
		return "", fmt.Errorf("unknown governance decision: %q", decision.DecisionValue)
	}

	out, runErr := tool.Run(ctx, input)
	// Every tool output -- native, shell, or MCP -- is appended to the
	// conversation and sent to the model provider. Mask credentials first.
	out = truncateOutput(redact.String(out), r.sre.MaxToolOutputBytes)
	if runErr != nil {
		runErr = fmt.Errorf("%s", redact.String(runErr.Error()))
	}
	event.Executed = true
	if runErr != nil {
		event.Outcome = "error"
	} else {
		event.Outcome = "completed"
	}
	r.emit(event)
	return out, runErr
}

func (r *Registry) emit(event AuditEvent) {
	if r.onEvent != nil {
		r.onEvent(event)
	}
}

func confirmInTerminal(decision governance.Decision, response *governance.EvaluateResponse) bool {
	fmt.Fprintf(os.Stderr, "\n[loom] DevMind: %s (risk_score=%.1f)\n", decision, response.RiskScore)
	if response.AuditID != "" {
		fmt.Fprintf(os.Stderr, "  audit_id: %s\n", response.AuditID)
	}
	for _, why := range response.Why {
		fmt.Fprintf(os.Stderr, "  - %s\n", why)
	}
	fmt.Fprint(os.Stderr, "Continue anyway? [y/N]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "y"
}

// isShellTool reports whether name is one of the general-purpose shell
// tools, whose free-text commands get infra classification before governance.
func isShellTool(name string) bool {
	return name == config.ShellPowerShell || name == config.ShellBash
}

// resolvePowerShellBinary finds a PowerShell executable on PATH. PowerShell
// 7+ ("pwsh") is preferred on every OS because it is the one binary that
// actually exists cross-platform; Windows PowerShell 5.1 ("powershell.exe")
// is only used as a fallback on Windows when pwsh was not installed.
func resolvePowerShellBinary() (string, error) {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("PowerShell (pwsh) not found in PATH; install PowerShell 7+ (https://aka.ms/powershell)")
}

// resolveBashBinary finds bash on PATH.
//
// On Windows, %SystemRoot%\System32\bash.exe is the WSL launcher, which
// usually comes first on PATH and fails outright when no WSL distribution
// is installed (on a Spanish-language Windows the message reads "El
// subsistema de Windows para Linux no tiene distribuciones instaladas"). It is skipped in favor of a real bash such
// as Git Bash, including Git's default install locations when Git's bin
// directory is not on PATH.
func resolveBashBinary() (string, error) {
	if runtime.GOOS != "windows" {
		if path, err := exec.LookPath("bash"); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("bash not found in PATH; install bash or set \"sre.shell\": \"powershell\"")
	}
	var candidates []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidates = append(candidates, filepath.Join(dir, "bash.exe"))
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, "Git", "bin", "bash.exe"), filepath.Join(base, "Programs", "Git", "bin", "bash.exe"))
		}
	}
	if path := pickWindowsBash(candidates, os.Getenv("SystemRoot"), fileExists); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("bash (Git Bash) not found on Windows; System32\\bash.exe is the WSL launcher and is not used. Install Git for Windows or set \"sre.shell\": \"powershell\"")
}

// pickWindowsBash returns the first existing candidate that is not the WSL
// launcher under systemRoot.
func pickWindowsBash(candidates []string, systemRoot string, exists func(string) bool) string {
	wsl := ""
	if systemRoot != "" {
		wsl = strings.ToLower(filepath.Clean(filepath.Join(systemRoot, "System32", "bash.exe")))
	}
	for _, candidate := range candidates {
		clean := strings.ToLower(filepath.Clean(candidate))
		if clean == wsl || strings.HasSuffix(clean, `\system32\bash.exe`) || strings.HasSuffix(clean, `\sysnative\bash.exe`) {
			continue
		}
		if exists(candidate) {
			return candidate
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// shellTool builds the general-purpose shell tool for the configured shell.
// Only one is registered, so the model is never offered a shell whose
// syntax doesn't match what will actually run.
func shellTool(shell string) Tool {
	label, resolve := "PowerShell", resolvePowerShellBinary
	argv := func(command string) []string { return []string{"-NoProfile", "-NonInteractive", "-Command", command} }
	if shell == config.ShellBash {
		label, resolve = "bash", resolveBashBinary
		// --noprofile/--norc: no user dotfiles altering behavior; -o pipefail
		// so a failure inside a pipeline is reported, not masked by `| head`.
		argv = func(command string) []string {
			return []string{"--noprofile", "--norc", "-o", "pipefail", "-c", command}
		}
	}
	return Tool{
		Name: shell,
		Description: "Runs a " + label + " command (terraform/tofu, kubectl, helm, " +
			"aws/gcloud/az, runbook scripts, HTTP queries to Prometheus/Grafana/Datadog, " +
			"etc.) and returns stdout+stderr. Prefer the " +
			"native tools (k8s_get, k8s_describe, k8s_logs, cloud_read, " +
			"metrics_query) for read-only investigation; use " + shell + " when " +
			"you need something those tools do not cover. Every infrastructure " +
			"command is classified and evaluated by DevMind before it runs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The full " + label + " command to run, exactly as it would be typed in a terminal.",
				},
			},
			"required": []string{"command"},
		},
		// The shell tool is Loom's general-purpose escape hatch: it can run
		// absolutely anything, including reads. There is no reliable way to
		// tell a read command from a mutation by string-matching alone, and
		// getting that wrong in the mutating direction defeats --dry-run
		// entirely. So: always treated as mutating for dry-run purposes,
		// same "don't trust an unlabelled action" default already used for
		// MCP tools without annotations. A rehearsal that needs a read
		// covered by this tool should be re-run without --dry-run, or use
		// one of the native read-only tools instead.
		IsMutating: func(map[string]any) bool { return true },
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			command, _ := input["command"].(string)
			if command == "" {
				return "", fmt.Errorf("missing 'command'")
			}
			bin, err := resolve()
			if err != nil {
				return "", err
			}
			cmd := exec.CommandContext(ctx, bin, argv(command)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out), fmt.Errorf("command failed: %w", err)
			}
			return string(out), nil
		}}
}

func readFileTool() Tool {
	return Tool{
		Name: "read_file",
		Description: "Reads a text file: Kubernetes manifests, Terraform modules, " +
			"Helm charts, CI/CD configs, " +
			"runbooks, exported logs, etc.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path of the file to read, relative or absolute.",
				},
			},
			"required": []string{"path"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			path, _ := input["path"].(string)
			if path == "" {
				return "", fmt.Errorf("missing 'path'")
			}
			if err := checkSensitivePath(path); err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}}
}

func writeFileTool() Tool {
	return Tool{
		Name: "write_file",
		Description: "Writes content to a file (manifests, Terraform modules, " +
			"Helm values, runbooks, etc.), overwriting it if it exists.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path of the file to write, relative or absolute.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full content to write to the file.",
				},
			},
			"required": []string{"path", "content"},
		},
		// write_file always mutates the filesystem -- never ambiguous, so
		// unlike the shell tool this doesn't need the conservative default,
		// it just is one.
		IsMutating: func(map[string]any) bool { return true },
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			path, _ := input["path"].(string)
			content, _ := input["content"].(string)
			if path == "" {
				return "", fmt.Errorf("missing 'path'")
			}
			if err := checkSensitivePath(path); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %s (%d bytes)", path, len(content)), nil
		}}
}

// updatePlanTool declares or updates the step plan of an operations task
// (e.g. an incident runbook). It runs nothing by itself: the agent loop
// (internal/agent) intercepts the call by name to emit an EventPlan; Run
// only acknowledges it, so the audit log still gets an entry.
func updatePlanTool() Tool {
	return Tool{
		Name: "update_plan",
		Description: "Declares or updates the step plan for the current task " +
			"(e.g. the steps of an incident runbook or an infrastructure change). " +
			"Call it before any task with more than one step, and again every time " +
			"a step changes status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{
					"type":        "array",
					"description": "Ordered list of plan steps.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title": map[string]any{
								"type":        "string",
								"description": "Short description of the step.",
							},
							"status": map[string]any{
								"type":        "string",
								"description": "One of: pending, in_progress, done.",
								"enum":        []string{"pending", "in_progress", "done"},
							},
						},
						"required": []string{"title", "status"},
					},
				},
			},
			"required": []string{"steps"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			steps, _ := input["steps"].([]any)
			if len(steps) == 0 {
				return "", fmt.Errorf("missing 'steps'")
			}
			return fmt.Sprintf("plan actualizado: %d pasos", len(steps)), nil
		}}
}

// -- Native SRE investigation tools --------------------------------------
//
// These call the underlying binaries directly via exec.Command with an
// argument slice (no shell involved), which is deliberate: it removes an
// entire class of quoting/injection bugs that "powershell" with a
// string-interpolated command cannot avoid, and it lets the model reach
// for a structured, read-only action instead of reconstructing the
// equivalent kubectl/cloud-CLI invocation by hand every time.

// runBinary is the shared exec plumbing for the native tools below.
func runBinary(ctx context.Context, bin string, args ...string) (string, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%s not found in PATH", bin)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s failed: %w", bin, err)
	}
	return string(out), nil
}

func k8sGetTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "k8s_get",
		Description: "Lists Kubernetes resources (equivalent to 'kubectl get'). " +
			"Read-only. When 'output' is 'json' and the resource type is one of " +
			"pod/deployment/replicaset/statefulset/daemonset/node/service/event, the " +
			"output is condensed to a readable table (name, status, restarts, age, " +
			"etc.) instead of the full manifest; use 'raw: true' to get the " +
			"full, uncondensed JSON.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":       map[string]any{"type": "string", "description": "Resource type, e.g. pods, deployments, nodes, events."},
				"name":           map[string]any{"type": "string", "description": "Specific resource name (optional)."},
				"namespace":      map[string]any{"type": "string", "description": "Namespace (optional; empty uses the default namespace)."},
				"all_namespaces": map[string]any{"type": "boolean", "description": "If true, adds -A (all namespaces)."},
				"output":         map[string]any{"type": "string", "description": "Output format: wide, json, yaml (optional)."},
				"raw":            map[string]any{"type": "boolean", "description": "If true, do not condense 'json' output -- return the full manifest."},
			},
			"required": []string{"resource"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			resource, _ := input["resource"].(string)
			if resource == "" {
				return "", fmt.Errorf("missing 'resource'")
			}
			args := []string{"get", resource}
			if name, _ := input["name"].(string); name != "" {
				args = append(args, name)
			}
			if sre.KubeContext != "" {
				args = append(args, "--context", sre.KubeContext)
			}
			if allNS, _ := input["all_namespaces"].(bool); allNS {
				args = append(args, "-A")
			} else if ns, _ := input["namespace"].(string); ns != "" {
				args = append(args, "-n", ns)
			}
			output, _ := input["output"].(string)
			if output != "" {
				args = append(args, "-o", output)
			}
			out, err := runBinary(ctx, "kubectl", args...)
			if err != nil {
				return out, err
			}
			raw, _ := input["raw"].(bool)
			if raw || output != "json" {
				return out, nil
			}
			if summary, ok := summarizeK8sJSON(resource, []byte(out)); ok {
				return summary, nil
			}
			return out, nil
		}}
}

func k8sDescribeTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "k8s_describe",
		Description: "Describes a Kubernetes resource (equivalent to 'kubectl describe'), " +
			"including recent events. Read-only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":  map[string]any{"type": "string", "description": "Resource type, e.g. pod, deployment, node."},
				"name":      map[string]any{"type": "string", "description": "Resource name."},
				"namespace": map[string]any{"type": "string", "description": "Namespace (optional)."},
			},
			"required": []string{"resource", "name"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			resource, _ := input["resource"].(string)
			name, _ := input["name"].(string)
			if resource == "" || name == "" {
				return "", fmt.Errorf("missing 'resource' and/or 'name'")
			}
			args := []string{"describe", resource, name}
			if sre.KubeContext != "" {
				args = append(args, "--context", sre.KubeContext)
			}
			if ns, _ := input["namespace"].(string); ns != "" {
				args = append(args, "-n", ns)
			}
			return runBinary(ctx, "kubectl", args...)
		}}
}

func k8sLogsTool(sre config.SreConfig) Tool {
	return Tool{
		Name:        "k8s_logs",
		Description: "Fetches pod logs (equivalent to 'kubectl logs'). Read-only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pod":        map[string]any{"type": "string", "description": "Pod name."},
				"namespace":  map[string]any{"type": "string", "description": "Namespace (optional)."},
				"container":  map[string]any{"type": "string", "description": "Specific container (optional, for multi-container pods)."},
				"previous":   map[string]any{"type": "boolean", "description": "If true, logs of the previous container (--previous), useful after a crash."},
				"tail_lines": map[string]any{"type": "integer", "description": "Number of trailing lines to return (optional)."},
				"since":      map[string]any{"type": "string", "description": "Time window, e.g. '15m', '1h' (optional)."},
			},
			"required": []string{"pod"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			pod, _ := input["pod"].(string)
			if pod == "" {
				return "", fmt.Errorf("missing 'pod'")
			}
			args := []string{"logs", pod}
			if sre.KubeContext != "" {
				args = append(args, "--context", sre.KubeContext)
			}
			if ns, _ := input["namespace"].(string); ns != "" {
				args = append(args, "-n", ns)
			}
			if container, _ := input["container"].(string); container != "" {
				args = append(args, "-c", container)
			}
			if previous, _ := input["previous"].(bool); previous {
				args = append(args, "--previous")
			}
			if since, _ := input["since"].(string); since != "" {
				args = append(args, "--since", since)
			}
			if tail, ok := input["tail_lines"].(float64); ok && tail > 0 {
				args = append(args, "--tail", fmt.Sprintf("%d", int(tail)))
			}
			return runBinary(ctx, "kubectl", args...)
		}}
}

// cloud_read is documented and schema'd to the model as read-only, and the
// local policy approves it without asking. So the tool itself only runs
// commands it recognizes as reads, per provider, and refuses everything
// else -- an unknown verb is rejected, not assumed safe. This runs BEFORE
// the CLI is invoked; it never replaces governance, which still evaluates
// every call. Anything it refuses can still be run through the shell tool,
// where governance sees it as a possible change.

// awsReadPrefixes are the AWS CLI operation prefixes that only read.
var awsReadPrefixes = []string{"describe-", "list-", "get-", "lookup-", "search-", "batch-get-", "filter-"}

// awsSecretReads return credentials or secret material even though they
// are reads: their output must not be fed to the model through cloud_read.
var awsSecretReads = map[string]bool{
	"get-secret-value": true, "batch-get-secret-value": true,
	"get-session-token": true, "get-federation-token": true,
	"get-login-password": true, "get-authorization-token": true,
	"get-token": true, "get-role-credentials": true,
	"get-credentials-for-identity": true,
}

// cloudReadVerbs are the gcloud / az command verbs that only read.
var cloudReadVerbs = map[string]map[string]bool{
	"gcloud": {"list": true, "describe": true, "get-iam-policy": true, "read": true},
	"az":     {"list": true, "show": true},
}

// cloudMutatingHints guard one ambiguity of the verb-position rule: a
// resource that happens to be named like a read verb ("gcloud compute
// instances delete list"). They are a second check on top of the read
// allowlist, never the decision on their own.
var cloudMutatingHints = map[string]bool{
	"create": true, "delete": true, "remove": true, "update": true, "patch": true,
	"set": true, "add": true, "start": true, "stop": true, "restart": true,
	"reset": true, "resize": true, "suspend": true, "resume": true, "deploy": true,
	"deallocate": true, "redeploy": true, "import": true, "export": true,
	"move": true, "restore": true, "cancel": true, "enable": true, "disable": true,
	"attach": true, "detach": true, "ssh": true, "scp": true, "rm": true, "cp": true,
}

// azSecretReads are az command paths whose output is secret material.
var azSecretReads = []string{
	"keyvault secret show", "keyvault secret download", "keys list",
	"show-connection-string", "list-connection-strings", "list-keys",
	"credential show", "get-credentials",
}

// cloudCommandPath returns the command words of a cloud CLI invocation:
// leading global flags are skipped (with their value when they take one),
// then every word up to the first flag is collected. "aws --region x ec2
// describe-instances --output json" -> [ec2 describe-instances].
func cloudCommandPath(args []string) []string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if !strings.Contains(args[i], "=") && cloudValueFlags[args[i]] {
			i++
		}
		i++
	}
	var path []string
	for ; i < len(args) && !strings.HasPrefix(args[i], "-"); i++ {
		path = append(path, strings.ToLower(args[i]))
	}
	return path
}

// isCloudReadOnlyCommand reports whether args (without the binary) is a
// command cloud_read recognizes as a pure read for provider. Anything it
// does not recognize returns false.
func isCloudReadOnlyCommand(provider string, args []string) bool {
	path := cloudCommandPath(args)
	if len(path) < 2 {
		return false
	}
	switch provider {
	case "aws":
		service, op := path[0], path[1]
		if service == "s3" {
			return op == "ls"
		}
		if awsSecretReads[op] {
			return false
		}
		if service == "ssm" && strings.HasPrefix(op, "get-parameter") && containsAny(args, "--with-decryption") {
			return false
		}
		for _, prefix := range awsReadPrefixes {
			if strings.HasPrefix(op, prefix) {
				return true
			}
		}
		return false
	case "gcloud", "az":
		joined := " " + strings.Join(path, " ") + " "
		if provider == "az" {
			for _, secret := range azSecretReads {
				if strings.Contains(joined, " "+secret+" ") {
					return false
				}
			}
		}
		verbs := cloudReadVerbs[provider]
		verbAt := -1
		switch {
		case verbs[path[len(path)-1]]:
			verbAt = len(path) - 1
		case len(path) >= 3 && verbs[path[len(path)-2]]:
			// "gcloud compute instances describe web-1": verb, then one resource.
			verbAt = len(path) - 2
		default:
			return false
		}
		for _, word := range path[:verbAt] {
			if cloudMutatingHints[word] {
				return false
			}
		}
		return true
	}
	return false
}

func cloudReadTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "cloud_read",
		Description: "Runs a read-only command (list/describe/get/show) against " +
			"aws, gcloud, or az. The tool itself only runs operations it recognizes " +
			"as reads (aws describe-*/list-*/get-*, gcloud list/describe, az list/show) and " +
			"refuses everything else, including reads that return secrets, as a defense in " +
			"depth on top of governance -- not a replacement for it. For real changes use " +
			"the shell tool, which also gets the correct blast radius in governance.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{"type": "string", "description": "aws, gcloud, or az.", "enum": []string{"aws", "gcloud", "az"}},
				"args":     map[string]any{"type": "array", "description": "Arguments exactly as they would be passed to the CLI, e.g. ['ec2','describe-instances'].", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"provider", "args"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			provider, _ := input["provider"].(string)
			if sre.Cloud.Provider != "" {
				provider = sre.Cloud.Provider
			}
			if provider != "aws" && provider != "gcloud" && provider != "az" {
				return "", fmt.Errorf("'provider' must be aws, gcloud, or az")
			}
			rawArgs, _ := input["args"].([]any)
			if len(rawArgs) == 0 {
				return "", fmt.Errorf("missing 'args'")
			}
			args := make([]string, 0, len(rawArgs))
			for _, a := range rawArgs {
				s, _ := a.(string)
				args = append(args, s)
			}
			if !isCloudReadOnlyCommand(provider, args) {
				return "", fmt.Errorf("cloud_read is read-only and only runs known read operations (describe/list/get/show), not %v -- use the shell tool instead, which goes through governance as a possible change", args)
			}
			if sre.Cloud.Profile != "" {
				switch provider {
				case "aws":
					args = append(args, "--profile", sre.Cloud.Profile)
				case "az":
					args = append(args, "--subscription", sre.Cloud.Profile)
				}
			}
			return runBinary(ctx, provider, args...)
		}}
}

func metricsQueryTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "metrics_query",
		Description: "Queries observability metrics (Prometheus/Grafana/Datadog) " +
			"using the endpoints configured in loom.config.json (sre.observability). " +
			"Read-only. Vector/scalar responses (instant queries) " +
			"are condensed to a METRIC/VALUE table; range/matrix responses are returned " +
			"in full because condensing them would lose the time series. Use 'raw: true' " +
			"to always get the full API JSON.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"system": map[string]any{"type": "string", "description": "prometheus, grafana, or datadog.", "enum": []string{"prometheus", "grafana", "datadog"}},
				"query":  map[string]any{"type": "string", "description": "Query expression (PromQL for prometheus/grafana, metric query syntax for datadog)."},
				"raw":    map[string]any{"type": "boolean", "description": "If true, returns the full API JSON instead of the tabular summary."},
			},
			"required": []string{"system", "query"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			system, _ := input["system"].(string)
			query, _ := input["query"].(string)
			if system == "" || query == "" {
				return "", fmt.Errorf("missing 'system' and/or 'query'")
			}
			raw, _ := input["raw"].(bool)
			switch system {
			case "prometheus":
				if sre.Observability.PrometheusURL == "" {
					return "", fmt.Errorf("sre.observability.prometheus_url is not set")
				}
				url := fmt.Sprintf("%s/api/v1/query?query=%s", strings.TrimRight(sre.Observability.PrometheusURL, "/"), queryEscape(query))
				body, err := httpGet(ctx, url, nil)
				if err != nil {
					return formatObservabilityResponse(body, true, nil), err
				}
				return formatObservabilityResponse(body, raw, summarizePrometheusResponse), nil
			case "grafana":
				if sre.Observability.GrafanaURL == "" {
					return "", fmt.Errorf("sre.observability.grafana_url is not set")
				}
				headers := map[string]string{}
				if sre.Observability.GrafanaTokenEnv != "" {
					if token := os.Getenv(sre.Observability.GrafanaTokenEnv); token != "" {
						headers["Authorization"] = "Bearer " + token
					}
				}
				url := fmt.Sprintf("%s/api/datasources/proxy/1/api/v1/query?query=%s", strings.TrimRight(sre.Observability.GrafanaURL, "/"), queryEscape(query))
				body, err := httpGet(ctx, url, headers)
				if err != nil {
					return formatObservabilityResponse(body, true, nil), err
				}
				return formatObservabilityResponse(body, raw, summarizePrometheusResponse), nil
			case "datadog":
				site := sre.Observability.DatadogSite
				if site == "" {
					site = "datadoghq.com"
				}
				headers := map[string]string{}
				if sre.Observability.DatadogAPIKeyEnv != "" {
					headers["DD-API-KEY"] = os.Getenv(sre.Observability.DatadogAPIKeyEnv)
				}
				if sre.Observability.DatadogAppKeyEnv != "" {
					headers["DD-APPLICATION-KEY"] = os.Getenv(sre.Observability.DatadogAppKeyEnv)
				}
				url := fmt.Sprintf("https://api.%s/api/v1/query?query=%s", site, queryEscape(query))
				body, err := httpGet(ctx, url, headers)
				if err != nil {
					return formatObservabilityResponse(body, true, nil), err
				}
				return formatObservabilityResponse(body, raw, summarizeDatadogResponse), nil
			default:
				return "", fmt.Errorf("'system' must be prometheus, grafana, or datadog")
			}
		}}
}

func queryEscape(q string) string {
	// Minimal, dependency-free query escaping for the common characters
	// that show up in PromQL (spaces, braces, quotes).
	replacer := strings.NewReplacer(
		" ", "%20", "{", "%7B", "}", "%7D", "\"", "%22", "=", "%3D",
	)
	return replacer.Replace(q)
}

// httpGet returns the raw response body regardless of status: callers
// decide how to present an error body (formatObservabilityResponse can
// still pretty-print it) instead of this function making that call once
// for everyone.
func httpGet(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("query returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}
