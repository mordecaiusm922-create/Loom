package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"loom/internal/config"
	"loom/internal/governance"
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
	r.register(powershellTool())
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
		return fmt.Errorf("herramienta externa invalida")
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("la herramienta %q ya esta registrada", tool.Name)
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
		return "", fmt.Errorf("herramienta desconocida: %s", name)
	}

	env := ResolveEnvironment(name, input, r.sre)
	if r.dryRun && tool.IsMutating != nil && tool.IsMutating(input) {
		event := AuditEvent{Time: time.Now().UTC(), SessionID: r.sessionID, Tool: name, Engine: "dry-run", Outcome: "dry_run_skipped", Environment: env.Tier}
		r.emit(event)
		return fmt.Sprintf("[dry-run] %s no se ejecuto (accion mutante); gobernanza no fue consultada", name), nil
	}

	affectsProduction := declaredProduction || env.Tier == config.TierProd
	var decision *governance.EvaluateResponse
	var err error
	engineUsed := "policy"
	if name == "powershell" {
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
		return "", fmt.Errorf("error consultando gobernanza: %w", err)
	}
	event := AuditEvent{Time: time.Now().UTC(), SessionID: r.sessionID, Tool: name, Engine: engineUsed, Decision: decision.DecisionValue, RiskScore: decision.RiskScore, Environment: env.Tier}
	switch decision.DecisionValue {
	case governance.Block:
		event.Outcome = "blocked"
		r.emit(event)
		return "", fmt.Errorf("BLOQUEADO por DevMind: %s", strings.Join(decision.Why, "; "))
	case governance.Escalate, governance.Review:
		if r.nonInteractive {
			event.Outcome = "human_review_required"
			r.emit(event)
			return "", fmt.Errorf("accion requiere revision humana (%s) y no se pudo confirmar en un entorno no interactivo", decision.DecisionValue)
		}
		if !r.confirm(decision.DecisionValue, decision) {
			event.Outcome = "cancelled"
			r.emit(event)
			return "", fmt.Errorf("accion cancelada por el usuario tras aviso de %s", decision.DecisionValue)
		}
	case governance.Rewrite:
		event.Outcome = "rewrite_required"
		r.emit(event)
		return "", fmt.Errorf("DevMind requiere reescribir esta accion; Loom no ejecuta REWRITE automaticamente")
	case governance.Allow:
		// continue
	default:
		event.Outcome = "invalid_decision"
		r.emit(event)
		return "", fmt.Errorf("decision de gobernanza desconocida: %q", decision.DecisionValue)
	}

	out, runErr := tool.Run(ctx, input)
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
	for _, why := range response.Why {
		fmt.Fprintf(os.Stderr, "  - %s\n", why)
	}
	fmt.Fprint(os.Stderr, "Continuar de todos modos? [y/N]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "y"
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
	return "", fmt.Errorf("no se encontro PowerShell (pwsh) en PATH; instala PowerShell 7+ (https://aka.ms/powershell)")
}

func powershellTool() Tool {
	return Tool{
		Name: "powershell",
		Description: "Ejecuta un comando de PowerShell (terraform/tofu, kubectl, helm, " +
			"aws/gcloud/az, scripts de runbook, consultas a Prometheus/Grafana/Datadog " +
			"via Invoke-RestMethod, etc.) y retorna stdout+stderr. Prefiere las " +
			"herramientas nativas (k8s_get, k8s_describe, k8s_logs, cloud_read, " +
			"metrics_query) para investigacion de solo lectura; usa powershell cuando " +
			"necesites algo que esas herramientas no cubren. Cada comando de " +
			"infraestructura es clasificado y evaluado por DevMind antes de ejecutarse.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "El comando de PowerShell completo a ejecutar, tal cual se escribiria en la terminal.",
				},
			},
			"required": []string{"command"},
		},
		// powershell is Loom's general-purpose escape hatch: it can run
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
				return "", fmt.Errorf("falta 'command'")
			}
			bin, err := resolvePowerShellBinary()
			if err != nil {
				return "", err
			}
			cmd := exec.CommandContext(ctx, bin, "-NoProfile", "-NonInteractive", "-Command", command)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out), fmt.Errorf("comando fallo: %w", err)
			}
			return string(out), nil
		}}
}

func readFileTool() Tool {
	return Tool{
		Name: "read_file",
		Description: "Lee el contenido de un archivo de texto: manifiestos de " +
			"Kubernetes, modulos de Terraform, charts de Helm, configs de CI/CD, " +
			"runbooks, logs exportados, etc.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Ruta del archivo a leer, relativa o absoluta.",
				},
			},
			"required": []string{"path"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			path, _ := input["path"].(string)
			if path == "" {
				return "", fmt.Errorf("falta 'path'")
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
		Description: "Escribe contenido en un archivo (manifiestos, modulos de " +
			"Terraform, values de Helm, runbooks, etc.), sobrescribiendo si existe.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Ruta del archivo a escribir, relativa o absoluta.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Contenido completo a escribir en el archivo.",
				},
			},
			"required": []string{"path", "content"},
		},
		// write_file always mutates the filesystem -- never ambiguous, so
		// unlike powershell this doesn't need the conservative default,
		// it just is one.
		IsMutating: func(map[string]any) bool { return true },
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			path, _ := input["path"].(string)
			content, _ := input["content"].(string)
			if path == "" {
				return "", fmt.Errorf("falta 'path'")
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("escrito: %s (%d bytes)", path, len(content)), nil
		}}
}

// updatePlanTool declares/actualiza el plan de pasos de una tarea de
// operacion (ej. un runbook de incidente). No ejecuta nada por si mismo --
// el loop del agente (internal/agent) intercepta la llamada por nombre para
// emitir un EventPlan; el Run aqui solo confirma que el tool existe y
// devuelve un ack, para que el registro de auditoria tenga una entrada.
func updatePlanTool() Tool {
	return Tool{
		Name: "update_plan",
		Description: "Declara o actualiza el plan de pasos de la tarea actual " +
			"(ej. los pasos de un runbook de incidente o un cambio de infraestructura). " +
			"Llamalo antes de cualquier tarea de mas de un paso, y de nuevo cada vez " +
			"que un paso cambie de estado.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{
					"type":        "array",
					"description": "Lista ordenada de pasos del plan.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title": map[string]any{
								"type":        "string",
								"description": "Descripcion corta del paso.",
							},
							"status": map[string]any{
								"type":        "string",
								"description": "Uno de: pending, in_progress, done.",
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
				return "", fmt.Errorf("falta 'steps'")
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
		return "", fmt.Errorf("%s no encontrado en PATH", bin)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s fallo: %w", bin, err)
	}
	return string(out), nil
}

func k8sGetTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "k8s_get",
		Description: "Lista recursos de Kubernetes (equivalente a 'kubectl get'). " +
			"Solo lectura. Cuando 'output' es 'json' y el tipo de recurso es uno de " +
			"pod/deployment/replicaset/statefulset/daemonset/node/service/event, la " +
			"salida se condensa a una tabla legible (nombre, estado, reinicios, edad, " +
			"etc.) en vez del manifiesto completo; usa 'raw: true' para recuperar el " +
			"JSON completo sin condensar.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":       map[string]any{"type": "string", "description": "Tipo de recurso, ej. pods, deployments, nodes, events."},
				"name":           map[string]any{"type": "string", "description": "Nombre especifico del recurso (opcional)."},
				"namespace":      map[string]any{"type": "string", "description": "Namespace (opcional; vacio usa el namespace por defecto)."},
				"all_namespaces": map[string]any{"type": "boolean", "description": "Si es true, agrega -A (todos los namespaces)."},
				"output":         map[string]any{"type": "string", "description": "Formato de salida: wide, json, yaml (opcional)."},
				"raw":            map[string]any{"type": "boolean", "description": "Si es true, no condensar la salida 'json' -- devolver el manifiesto completo."},
			},
			"required": []string{"resource"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			resource, _ := input["resource"].(string)
			if resource == "" {
				return "", fmt.Errorf("falta 'resource'")
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
		Description: "Describe un recurso de Kubernetes (equivalente a 'kubectl describe'), " +
			"incluye eventos recientes. Solo lectura.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":  map[string]any{"type": "string", "description": "Tipo de recurso, ej. pod, deployment, node."},
				"name":      map[string]any{"type": "string", "description": "Nombre del recurso."},
				"namespace": map[string]any{"type": "string", "description": "Namespace (opcional)."},
			},
			"required": []string{"resource", "name"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			resource, _ := input["resource"].(string)
			name, _ := input["name"].(string)
			if resource == "" || name == "" {
				return "", fmt.Errorf("faltan 'resource' y/o 'name'")
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
		Description: "Obtiene logs de un pod (equivalente a 'kubectl logs'). Solo lectura.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pod":        map[string]any{"type": "string", "description": "Nombre del pod."},
				"namespace":  map[string]any{"type": "string", "description": "Namespace (opcional)."},
				"container":  map[string]any{"type": "string", "description": "Contenedor especifico (opcional, para pods multi-contenedor)."},
				"previous":   map[string]any{"type": "boolean", "description": "Si es true, logs del contenedor anterior (--previous), util tras un crash."},
				"tail_lines": map[string]any{"type": "integer", "description": "Numero de lineas finales a devolver (opcional)."},
				"since":      map[string]any{"type": "string", "description": "Ventana de tiempo, ej. '15m', '1h' (opcional)."},
			},
			"required": []string{"pod"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			pod, _ := input["pod"].(string)
			if pod == "" {
				return "", fmt.Errorf("falta 'pod'")
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

// cloudMutatingVerbs is a defense-in-depth blocklist: cloud_read is
// documented and schema'd as read-only, but a model can still try to pass
// a mutating subcommand. This check runs BEFORE governance ever sees the
// call, so a mutating attempt never reaches DevMind mislabeled as a read.
// It is not a substitute for governance -- it only prevents this specific
// tool from being used to bypass the intent it declares.
var cloudMutatingVerbs = map[string]bool{
	"create": true, "delete": true, "remove": true, "rm": true, "terminate": true,
	"put": true, "update": true, "set": true, "add": true, "attach": true,
	"detach": true, "modify": true, "rotate": true, "apply": true,
	"associate": true, "disassociate": true, "revoke": true, "grant": true,
	"run": true, "start": true, "stop": true, "reboot": true, "restart": true,
	"deploy": true, "purge": true, "drain": true, "cordon": true, "scale": true,
	"patch": true, "replace": true, "upgrade": true, "uninstall": true,
	"install": true, "destroy": true, "taint": true, "cp": true, "mv": true,
	"sync": true, "copy": true, "invoke": true, "restore": true, "import": true,
	"enable": true, "disable": true, "reset": true, "resize": true,
	"cancel": true, "send": true, "publish": true, "release": true,
	"allocate": true, "register": true, "deregister": true, "authorize": true,
	"tag": true, "untag": true, "ssh": true, "exec": true, "execute": true,
	"write": true,
}

// isCloudMutatingCommand checks subcommand tokens only. Flags (anything
// starting with "-") are skipped, and each token is split on "-" and matched
// segment by segment against cloudMutatingVerbs. A substring match over
// every argument (the previous implementation) rejected the most common read
// commands in the field: "--output" contains "put", "--format" contains
// "rm", "describe-addresses" contains "add", "list-attached-role-policies"
// contains "attach". Segment matching still catches hyphenated compounds
// such as "terminate-instances", "delete-db-instance" and
// "batch-delete-image". A flag value that happens to be a verb (e.g. a
// bucket named "delete-me") is still rejected -- that errs toward refusal.
func isCloudMutatingCommand(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		for _, segment := range strings.Split(strings.ToLower(arg), "-") {
			if cloudMutatingVerbs[segment] {
				return true
			}
		}
	}
	return false
}

func cloudReadTool(sre config.SreConfig) Tool {
	return Tool{
		Name: "cloud_read",
		Description: "Ejecuta un comando de solo lectura (list/describe/get/show) contra " +
			"aws, gcloud, o az. Rechaza en el propio tool cualquier subcomando que " +
			"parezca mutar estado (create/delete/update/etc.), como defensa adicional " +
			"a la evaluacion de gobernanza -- no la reemplaza. Para cambios reales usa " +
			"powershell, que ademas obtiene el blast-radius correcto en gobernanza.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{"type": "string", "description": "aws, gcloud, o az.", "enum": []string{"aws", "gcloud", "az"}},
				"args":     map[string]any{"type": "array", "description": "Argumentos tal cual se pasarian a la CLI, ej. ['ec2','describe-instances'].", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"provider", "args"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			provider, _ := input["provider"].(string)
			if sre.Cloud.Provider != "" {
				provider = sre.Cloud.Provider
			}
			if provider != "aws" && provider != "gcloud" && provider != "az" {
				return "", fmt.Errorf("'provider' debe ser aws, gcloud, o az")
			}
			rawArgs, _ := input["args"].([]any)
			if len(rawArgs) == 0 {
				return "", fmt.Errorf("falta 'args'")
			}
			args := make([]string, 0, len(rawArgs))
			for _, a := range rawArgs {
				s, _ := a.(string)
				args = append(args, s)
			}
			if isCloudMutatingCommand(args) {
				return "", fmt.Errorf("cloud_read es solo lectura; %v parece un comando de escritura -- usa powershell si de verdad quieres mutar estado (pasara por gobernanza)", args)
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
		Description: "Consulta metricas de observabilidad (Prometheus/Grafana/Datadog) " +
			"usando los endpoints configurados en loom.config.json (sre.observability). " +
			"Solo lectura. Las respuestas de tipo vector/scalar (consultas instantaneas) " +
			"se condensan a una tabla METRIC/VALUE; las de tipo rango/matrix se devuelven " +
			"completas porque condensarlas perderia la serie temporal. Usa 'raw: true' " +
			"para recibir siempre el JSON completo de la API.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"system": map[string]any{"type": "string", "description": "prometheus, grafana, o datadog.", "enum": []string{"prometheus", "grafana", "datadog"}},
				"query":  map[string]any{"type": "string", "description": "Expresion de consulta (PromQL para prometheus/grafana, sintaxis de metric query para datadog)."},
				"raw":    map[string]any{"type": "boolean", "description": "Si es true, devuelve el JSON completo de la API en vez del resumen tabular."},
			},
			"required": []string{"system", "query"},
		},
		Run: func(ctx context.Context, input map[string]any) (string, error) {
			system, _ := input["system"].(string)
			query, _ := input["query"].(string)
			if system == "" || query == "" {
				return "", fmt.Errorf("faltan 'system' y/o 'query'")
			}
			raw, _ := input["raw"].(bool)
			switch system {
			case "prometheus":
				if sre.Observability.PrometheusURL == "" {
					return "", fmt.Errorf("sre.observability.prometheus_url no esta configurado")
				}
				url := fmt.Sprintf("%s/api/v1/query?query=%s", strings.TrimRight(sre.Observability.PrometheusURL, "/"), queryEscape(query))
				body, err := httpGet(ctx, url, nil)
				if err != nil {
					return formatObservabilityResponse(body, true, nil), err
				}
				return formatObservabilityResponse(body, raw, summarizePrometheusResponse), nil
			case "grafana":
				if sre.Observability.GrafanaURL == "" {
					return "", fmt.Errorf("sre.observability.grafana_url no esta configurado")
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
				return "", fmt.Errorf("'system' debe ser prometheus, grafana, o datadog")
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
		return nil, fmt.Errorf("consulta fallo: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("consulta devolvio HTTP %d", resp.StatusCode)
	}
	return body, nil
}
