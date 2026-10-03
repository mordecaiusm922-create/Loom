package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
	"github.com/mordecaiusm922-create/loom/internal/mcp"
	"github.com/mordecaiusm922-create/loom/internal/providers"
	"github.com/mordecaiusm922-create/loom/internal/tools"
)

// systemPromptTemplate has two %[1]s slots for the configured shell tool
// name (powershell or bash) so the prompt never names a tool that is not
// registered. Build it with buildSystemPrompt.
const systemPromptTemplate = `Eres Loom, un agente de SRE / Platform Engineering operando en una terminal
sobre %[1]s.

Tu dominio es exclusivamente operacion de infraestructura y plataforma:
Terraform/OpenTofu, Kubernetes/Helm, CLIs de cloud (aws/gcloud/az), CI/CD,
observabilidad (Prometheus/Grafana/Datadog), gestion de incidentes,
runbooks, capacidad, migraciones de esquema y control de acceso (IAM,
rotacion de secretos). Si te piden trabajo de desarrollo de producto o
features de aplicacion sin relacion con infraestructura, confiabilidad u
operaciones de plataforma, dilo explicitamente y redirige a la parte del
pedido que si sea de tu dominio, en vez de improvisar fuera de el.

Para investigacion de solo lectura preferi las herramientas nativas
k8s_get, k8s_describe, k8s_logs, cloud_read, y metrics_query en vez de
%[1]s -- son mas seguras (no interpretan un shell) y le dan a
gobernanza una llamada mas clara de evaluar. Usa %[1]s cuando la
tarea requiera algo que esas herramientas no cubren, o para aplicar un
cambio real (terraform, kubectl apply/delete/scale/rollout, helm, etc.).

Para cualquier tarea con mas de un paso -- en especial un runbook de
incidente o un cambio de infraestructura -- primero llama a la herramienta
update_plan con la lista completa de pasos (estado "pending"). Actualiza el
plan (llamando a update_plan de nuevo) cada vez que un paso pase a
"in_progress" o "done" -- no lo narres solo en texto libre.

Antes de aplicar un cambio de infraestructura, prefiere primero un paso de
solo lectura (terraform plan, k8s_get/k8s_describe, cloud_read) para
confirmar el estado actual, salvo que la tarea sea explicitamente de
respuesta a incidente y el tiempo importe mas que la confirmacion previa.

Cada accion sera evaluada por el motor de gobernanza (DevMind) antes de
ejecutarse -- un BLOCK o una cancelacion de REVIEW/ESCALATE es una señal
real, no un obstaculo a evadir reformulando el comando. Despues de aplicar
un cambio, verifica su resultado (k8s_get del recurso, terraform plan sin
diff, metrics_query, etc.) antes de reportar la tarea como completa; no la
des por terminada solo porque dejaste de pedir herramientas.`

func buildSystemPrompt(shell string) string {
	return fmt.Sprintf(systemPromptTemplate, shell)
}

const maxIterations = 25

type Session struct {
	cfg         config.Config
	registry    *tools.Registry
	sessionID   string
	onEvent     func(Event)
	auditFile   *os.File
	mcpClients  []*mcp.StdioClient
	mcpWarnings []error
	dryRun      bool
	// history is the conversation carried between RunWithTask calls, so
	// `loom chat` is a conversation and not a series of unrelated one-shot
	// prompts. `loom run` makes a single call, so it starts empty.
	history []providers.Message
	// resolve is resolveProvider outside tests.
	resolve func(config.Config, string) (providers.Provider, error)
}

// NewSession wires a Registry to a governance engine and, best-effort, opens
// a local append-only audit log at .loom/sessions/<sessionID>.jsonl. That
// file is what makes a postmortem possible after the process exits: the
// terminal/TUI output is transient, this is not. It never contains the raw
// action payload, same redaction rule as AuditEvent itself.
func NewSession(cfg config.Config, agentID, sessionID string) (*Session, error) {
	var engine governance.Engine
	if cfg.Governance.Enabled && cfg.Governance.Engine == "devmind" {
		engine = governance.NewDevMindEngine(cfg.Governance.BaseURL, cfg.Governance.Token)
	} else if os.Getenv("LOOM_UNSAFE_DISABLE_GOVERNANCE") == "1" {
		engine = governance.NoopEngine{}
		fmt.Fprintln(os.Stderr, "[loom] WARNING: gobernanza deshabilitada por LOOM_UNSAFE_DISABLE_GOVERNANCE=1")
	} else {
		return nil, fmt.Errorf("la gobernanza es obligatoria; configura governance.enabled=true y engine=devmind, o usa LOOM_UNSAFE_DISABLE_GOVERNANCE=1 para un opt-out local explicito")
	}
	registry := tools.NewRegistry(engine, agentID, sessionID, cfg.Sre)
	session := &Session{cfg: cfg, registry: registry, sessionID: sessionID, onEvent: defaultObserver(sessionID), resolve: resolveProvider}
	// A misconfigured or unreachable MCP server must never block Loom from
	// starting: the native tools (powershell, k8s_*, cloud_read,
	// metrics_query) already cover the SRE surface on their own. Failures
	// are collected and warned about, not fatal -- see connectMCPServers.
	session.mcpWarnings = session.connectMCPServers()
	for _, warning := range session.mcpWarnings {
		fmt.Fprintf(os.Stderr, "[loom] WARNING: MCP: %v\n", warning)
	}
	session.auditFile = openAuditFile(sessionID)
	registry.SetObserver(session.traceTool)
	return session, nil
}

// MCPWarnings returns any errors hit while connecting configured MCP
// servers. A non-empty result does not mean the session failed to start --
// only that one or more integrations beyond the native tools are
// unavailable for it.
func (s *Session) MCPWarnings() []error { return s.mcpWarnings }

// openAuditFile is best-effort: an incident should never fail to start
// because a local log directory could not be created. A failure just means
// this session has no persisted timeline, and is printed once as a warning.
func openAuditFile(sessionID string) *os.File {
	dir := ".loom/sessions"
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[loom] WARNING: no se pudo crear %s: %v (esta sesion no quedara persistida)\n", dir, err)
		return nil
	}
	path := fmt.Sprintf("%s/%s.jsonl", dir, sessionID)
	// O_EXCL: a timeline belongs to exactly one session. Appending to an
	// existing file (the old behavior with PID-based IDs) silently merged
	// unrelated incidents into one postmortem record.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[loom] WARNING: no se pudo abrir %s: %v (esta sesion no quedara persistida)\n", path, err)
		return nil
	}
	return file
}

// Close flushes and closes the audit file. Callers should defer this right
// after NewSession succeeds.
func (s *Session) Close() error {
	for _, client := range s.mcpClients {
		_ = client.Close()
	}
	s.mcpClients = nil
	if s.auditFile == nil {
		return nil
	}
	return s.auditFile.Close()
}

// SetDryRun propagates rehearsal mode to the governed registry. It is kept
// on Session because both `loom run` and `loom chat` create Sessions.
func (s *Session) SetDryRun(dryRun bool) {
	s.dryRun = dryRun
	s.registry.SetDryRun(dryRun)
}

// SetNonInteractive makes REVIEW and ESCALATE fail closed rather than wait
// for terminal input. CI callers use it when no human can confirm an action.
func (s *Session) SetNonInteractive(nonInteractive bool) {
	s.registry.SetNonInteractive(nonInteractive)
}

// connectMCPServers starts every configured server and registers its tools.
// A single server's failure (bad config, binary not found, tools/list
// erroring, a name collision with an existing tool) is recorded as a
// warning and that server is skipped -- it never prevents the others, or
// the native tools, from being available. See NewSession for why this must
// not be fatal.
func (s *Session) connectMCPServers() []error {
	var warnings []error
	for _, server := range s.cfg.MCPServers {
		if strings.TrimSpace(server.Name) == "" || strings.TrimSpace(server.Command) == "" {
			warnings = append(warnings, fmt.Errorf("una entrada de mcp_servers no tiene name y/o command; se omite"))
			continue
		}
		client, err := mcp.StartStdioServer(server.Command, server.Args...)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("iniciando servidor MCP %q: %w", server.Name, err))
			continue
		}
		definitions, err := client.DiscoverTools()
		if err != nil {
			_ = client.Close()
			warnings = append(warnings, fmt.Errorf("descubriendo herramientas MCP de %q: %w", server.Name, err))
			continue
		}
		registeredAny := false
		for _, definition := range definitions {
			definition := definition
			if err := s.registry.Register(tools.Tool{
				Name:        definition.Name,
				Description: definition.Description,
				InputSchema: definition.InputSchema,
				IsMutating: func(map[string]any) bool {
					// Missing annotations are deliberately treated as mutating in
					// dry-run mode: do not trust an unlabelled remote action.
					return definition.Annotations.DestructiveHint || !definition.Annotations.ReadOnlyHint
				},
				Run: func(_ context.Context, input map[string]any) (string, error) {
					raw, err := client.CallTool(definition.Name, input)
					if err != nil {
						return "", err
					}
					var pretty bytes.Buffer
					if err := json.Indent(&pretty, raw, "", "  "); err == nil {
						return pretty.String(), nil
					}
					return string(raw), nil
				},
			}); err != nil {
				warnings = append(warnings, fmt.Errorf("registrando herramienta %q de %q: %w", definition.Name, server.Name, err))
				continue
			}
			registeredAny = true
		}
		if registeredAny {
			s.mcpClients = append(s.mcpClients, client)
		} else {
			// Every tool this server offered either failed to register or
			// it offered none -- nothing to keep the subprocess alive for.
			_ = client.Close()
			warnings = append(warnings, fmt.Errorf("servidor MCP %q no registro ninguna herramienta", server.Name))
		}
	}
	return warnings
}

// SetEventObserver replaces the default (print-to-stdout/stderr) event
// handler. loom chat uses this to route events into the TUI instead.
func (s *Session) SetEventObserver(fn func(Event)) {
	if fn != nil {
		s.onEvent = fn
	}
}

// SetConfirmation replaces the default (blocking terminal y/N prompt) used
// for REVIEW/ESCALATE decisions. loom chat uses this to show a modal instead
// of reading from stdin, which a raw-mode TUI program does not do.
func (s *Session) SetConfirmation(fn func(governance.Decision, *governance.EvaluateResponse) bool) {
	s.registry.SetConfirmation(fn)
}

func (s *Session) emit(e Event) {
	if s.onEvent != nil {
		s.onEvent(e)
	}
}

// ToolNames lists the registered tool names, in registration order isn't
// guaranteed (map iteration) -- callers that need a stable display order
// should sort. Used by loom chat to render the tools sidebar.
func (s *Session) ToolNames() []string {
	names := make([]string, 0, len(s.registry.List()))
	for _, t := range s.registry.List() {
		names = append(names, t.Name)
	}
	return names
}

// SessionID returns the session identifier this Session was created with.
func (s *Session) SessionID() string { return s.sessionID }

// ClearHistory forgets the conversation so far (`/clear` in loom chat).
// The persisted .jsonl timeline is not touched.
func (s *Session) ClearHistory() { s.history = nil }

func resolveProvider(cfg config.Config, name string) (providers.Provider, error) {
	if custom, ok := cfg.CustomProvider(name); ok {
		return providers.NewOpenAICompatibleProvider(
			custom.ID,
			custom.BaseURL,
			custom.Model,
			custom.APIKeyEnv,
			custom.SupportsTooling,
			custom.CostInputPerMTok,
			custom.CostOutputPerMTok,
		), nil
	}

	switch name {
	case "anthropic":
		return providers.NewAnthropicProvider(cfg.Providers.Anthropic.Model)
	case "ollama":
		return providers.NewOllamaProvider(cfg.Providers.Ollama.Model, cfg.Providers.Ollama.BaseURL), nil
	default:
		return nil, fmt.Errorf("provider desconocido en config: %s", name)
	}
}

func (s *Session) Run(ctx context.Context, userPrompt string) error {
	return s.RunWithTask(ctx, userPrompt, "")
}

// RunWithTask resolves a route for this request, rather than fixing a provider
// at session creation. --task makes cost/risk decisions reproducible in CI.
func (s *Session) RunWithTask(ctx context.Context, userPrompt, taskHint string) error {
	started := time.Now()
	providerName := ""
	inputCost, outputCost := 0.0, 0.0
	inputTokens, outputTokens := 0, 0
	defer func() {
		cost := float64(inputTokens)/1_000_000*inputCost + float64(outputTokens)/1_000_000*outputCost
		summary := Event{Type: EventSessionSummary, Provider: providerName, InputTokens: inputTokens, OutputTokens: outputTokens, EstimatedCostUSD: cost, Duration: time.Since(started)}
		s.emit(summary)
		s.persistSessionSummary(summary)
	}()
	route := s.cfg.RouteForPrompt(userPrompt, taskHint)
	provider, err := s.resolve(s.cfg, route.Provider)
	if err != nil {
		s.emit(Event{Type: EventError, Err: err})
		return err
	}
	providerName = provider.Name()
	inputCost, outputCost = provider.CostPerMTok()
	s.emit(Event{Type: EventRoute, Task: route.Task, Provider: provider.Name(), Reason: route.Reason})

	toolSpecs := make([]providers.ToolSpec, 0, len(s.registry.List()))
	if provider.SupportsTooling() {
		for _, tool := range s.registry.List() {
			toolSpecs = append(toolSpecs, providers.ToolSpec{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	messages := append(append([]providers.Message{}, s.history...), providers.Message{Role: "user", Content: userPrompt})
	// Whatever happens, the turn is kept in history. messages is always
	// protocol-valid at a return point (it ends in a user prompt or in tool
	// results), and an interrupted turn is closed with an assistant note so
	// the next user prompt keeps user/assistant alternation.
	fail := func(err error) error {
		s.emit(Event{Type: EventError, Err: err})
		s.history = append(messages, providers.Message{Role: "assistant", Content: "[turno interrumpido: " + err.Error() + "]"})
		return err
	}
	for iteration := 0; iteration < maxIterations; iteration++ {
		response, err := provider.Complete(ctx, providers.CompletionRequest{SystemPrompt: buildSystemPrompt(s.cfg.Sre.ShellName()), Messages: messages, Tools: toolSpecs})
		if err != nil {
			return fail(fmt.Errorf("error del provider %s: %w", provider.Name(), err))
		}
		inputTokens += response.Usage.InputTokens
		outputTokens += response.Usage.OutputTokens
		if response.Text != "" {
			s.emit(Event{Type: EventText, Text: response.Text})
		}
		if len(response.ToolCalls) == 0 {
			s.history = append(messages, providers.Message{Role: "assistant", Content: response.Text})
			s.emit(Event{Type: EventDone})
			return nil
		}
		if !provider.SupportsTooling() {
			return fail(fmt.Errorf("el provider %s devolvio tool calls sin soportar tooling", provider.Name()))
		}
		messages = append(messages, providers.Message{Role: "assistant", Content: response.Text, ToolCalls: response.ToolCalls})
		for _, call := range response.ToolCalls {
			s.emit(Event{Type: EventToolStart, Tool: call.Name})
			// The environment is resolved inside the registry from declared
			// sre.environments and the context/profile the native tools
			// inject -- never from a substring of the model's input.
			output, toolErr := s.registry.Execute(ctx, call.Name, call.Input, false)
			if toolErr != nil {
				output = "ERROR: " + toolErr.Error()
			} else if call.Name == "update_plan" {
				s.emit(Event{Type: EventPlan, Steps: parsePlanSteps(call.Input)})
			}
			messages = append(messages, providers.Message{Role: "tool", Content: output, ToolCallID: call.ID, ToolName: call.Name})
		}
	}
	return fail(fmt.Errorf("se alcanzo el limite de %d iteraciones sin completar la tarea", maxIterations))
}

type sessionSummaryRecord struct {
	EventType        string    `json:"event_type"`
	Time             time.Time `json:"time"`
	SessionID        string    `json:"session_id"`
	Provider         string    `json:"provider,omitempty"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	DurationMS       int64     `json:"duration_ms"`
}

func (s *Session) persistSessionSummary(event Event) {
	if s.auditFile == nil {
		return
	}
	record := sessionSummaryRecord{EventType: "session_summary", Time: time.Now().UTC(), SessionID: s.sessionID, Provider: event.Provider, InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, EstimatedCostUSD: event.EstimatedCostUSD, DurationMS: event.Duration.Milliseconds()}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = s.auditFile.Write(append(line, '\n'))
}

func (s *Session) traceTool(event tools.AuditEvent) {
	s.emit(Event{
		Type:      EventToolResult,
		Tool:      event.Tool,
		Engine:    event.Engine,
		Decision:  event.Decision,
		RiskScore: event.RiskScore,
		Executed:  event.Executed,
		Outcome:   event.Outcome,
	})
	s.persistAuditEvent(event)
}

// persistAuditEvent appends one JSON line per tool call to the session's
// audit file, when one was successfully opened. Same no-payload redaction
// as tools.AuditEvent -- this file is meant to be shareable in a postmortem
// without a second redaction pass.
func (s *Session) persistAuditEvent(event tools.AuditEvent) {
	if s.auditFile == nil {
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = s.auditFile.Write(line)
}

func parsePlanSteps(input map[string]any) []PlanStep {
	raw, _ := input["steps"].([]any)
	steps := make([]PlanStep, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := entry["title"].(string)
		status, _ := entry["status"].(string)
		if title == "" {
			continue
		}
		steps = append(steps, PlanStep{Title: title, Status: status})
	}
	return steps
}
