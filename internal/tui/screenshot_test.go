package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mordecaiusm922-create/loom/internal/agent"
	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
)

// TestDumpScreens renders the real loom chat View() in several states to
// LOOM_DUMP_SCREENS/<name>.ansi, for README screenshots. Skipped otherwise.
func TestDumpScreens(t *testing.T) {
	dir := os.Getenv("LOOM_DUMP_SCREENS")
	if dir == "" {
		t.Skip("set LOOM_DUMP_SCREENS to dump screens")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Setenv("LOOM_UNSAFE_DISABLE_GOVERNANCE", "1")
	work := t.TempDir()
	wd, _ := os.Getwd()
	_ = os.Chdir(work)
	defer os.Chdir(wd)

	cfg := config.Default()
	cfg.Governance.Enabled = false
	cfg.Sre.KubeContext = "prod-eks"
	session, err := agent.NewSession(cfg, "loom-sre", "sess-20261003T091512Z-7f3a9c01")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	newModel := func() *model { return buildScreenModel(cfg, session) }
	save := func(name string, m *model) {
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(m.View()), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Welcome screen.
	save("welcome", newModel())

	// 2. Investigation in progress.
	m := newModel()
	m.startRunForScreenshot("por que reinicia el pod checkout en payments?")
	for _, e := range []agent.Event{
		{Type: agent.EventRoute, Task: "incident_response", Provider: "anthropic"},
		{Type: agent.EventPlan, Steps: []agent.PlanStep{
			{Title: "Ver estado de los pods", Status: "done"},
			{Title: "Leer logs del contenedor previo", Status: "done"},
			{Title: "Revisar limites de memoria", Status: "in_progress"},
			{Title: "Proponer remediacion", Status: "pending"},
		}},
		{Type: agent.EventToolResult, Tool: "k8s_get", Engine: "policy", Decision: governance.Allow, RiskScore: 1.0, Outcome: "completed"},
		{Type: agent.EventText, Text: "checkout-7d9f tiene 14 reinicios en 30 min, todos OOMKilled."},
		{Type: agent.EventToolResult, Tool: "k8s_logs", Engine: "policy", Decision: governance.Allow, RiskScore: 1.2, Outcome: "completed"},
		{Type: agent.EventText, Text: "El ultimo log antes del crash: java.lang.OutOfMemoryError (heap 512Mi, limite del pod 512Mi)."},
		{Type: agent.EventToolStart, Tool: "k8s_describe"},
	} {
		m.applyEvent(e)
	}
	m.refreshTranscript()
	m.ticks, m.moodSince = 4, 0
	save("investigating", m)

	// 3. DevMind asks for review before a production change.
	m.applyEvent(agent.Event{Type: agent.EventText, Text: "Propongo subir el limite de memoria a 1Gi con kubectl patch."})
	m.refreshTranscript()
	req := confirmRequest{decision: governance.Review, response: &governance.EvaluateResponse{
		DecisionValue: governance.Review, RiskScore: 6.5, AuditID: "act-9c41e2",
		Why: []string{"k8s_manifest sobre produccion (prod-eks)", "cambio de recursos en un Deployment con trafico"},
	}}
	m.pendingConfirm = &req
	m.setMood(moodAlert, 0)
	m.ticks = m.moodSince + 9
	save("review", m)

	// 4. DevMind blocks a destructive action.
	m = newModel()
	m.startRunForScreenshot("borra el namespace payments y recrealo limpio")
	for _, e := range []agent.Event{
		{Type: agent.EventRoute, Task: "security_review", Provider: "anthropic"},
		{Type: agent.EventToolResult, Tool: "bash", Engine: "infra", Decision: governance.Block, RiskScore: 9.8, Outcome: "blocked"},
		{Type: agent.EventText, Text: "DevMind bloqueo `kubectl delete namespace payments` en produccion (blast radius: org). No voy a reintentar por otra via; si es necesario, hace falta un break-glass con justificacion."},
	} {
		m.applyEvent(e)
	}
	m.refreshTranscript()
	save("blocked", m)
}

// startRunForScreenshot mirrors startRun without launching the agent.
func (m *model) startRunForScreenshot(prompt string) {
	m.running = true
	m.setMood(moodThinking, 0)
	m.transcript = append(m.transcript, promptMarkStyle.Render("› ")+userLineStyle.Render(prompt))
}

func (m *model) refreshTranscript() {
	m.viewport.SetContent(m.renderTranscript())
	m.viewport.GotoBottom()
}

func buildScreenModel(cfg config.Config, session *agent.Session) *model {
	m := &model{cfg: cfg, session: session, sessionID: session.SessionID(), envName: "prod-eks", envTier: config.TierProd}
	ti := textinput.New()
	ti.Placeholder = "que investigamos?"
	ti.Prompt = ""
	m.input = ti
	m.spinner = spinner.New()
	m.viewport = viewport.New(80, 20)
	m.Update(tea.WindowSizeMsg{Width: 118, Height: 34})
	return m
}

// newScreenModel builds a 118x34 model over a real session with governance
// disabled (no network), for layout tests.
func newScreenModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("LOOM_UNSAFE_DISABLE_GOVERNANCE", "1")
	wd, _ := os.Getwd()
	_ = os.Chdir(t.TempDir())
	t.Cleanup(func() { _ = os.Chdir(wd) })
	cfg := config.Default()
	cfg.Governance.Enabled = false
	session, err := agent.NewSession(cfg, "loom-sre", "sess-layout-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return buildScreenModel(cfg, session)
}
