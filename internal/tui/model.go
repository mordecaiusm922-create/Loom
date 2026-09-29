// Package tui implements `loom chat`, an interactive Bubble Tea front-end
// over the exact same agent.Session used by `loom run`. It is a
// presentation layer, not a second agent implementation: it subscribes to
// the same Event stream and the same governance confirmation hook that the
// plain-text CLI already uses.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/agent"
	"loom/internal/config"
	"loom/internal/governance"
)

// confirmRequest crosses from the agent's background goroutine into the
// Bubble Tea event loop. reply is answered by the Update() key handler once
// the person presses y/n, which unblocks the agent goroutine.
type confirmRequest struct {
	decision governance.Decision
	response *governance.EvaluateResponse
	reply    chan bool
}

// message types delivered into Update()
type (
	agentEventMsg    agent.Event
	confirmMsg       confirmRequest
	runDoneMsg       struct{ err error }
	eventClosedMsg   struct{}
	confirmClosedMsg struct{}
)

type model struct {
	cfg       config.Config
	session   *agent.Session
	sessionID string

	eventCh   chan agent.Event
	confirmCh chan confirmRequest
	doneCh    chan error

	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	width, height int
	ready         bool

	running        bool
	pendingConfirm *confirmRequest
	lastErr        error

	// sidebar state, all derived from the Event stream
	lastTask     string
	lastProvider string
	lastReason   string
	plan         []agent.PlanStep
	toolCalls    int
	lastDecision string
	lastRisk     float64

	transcript []string
}

// Run starts the interactive TUI. It blocks until the person quits.
func Run(cfg config.Config, agentID, sessionID string, dryRun bool) error {
	session, err := agent.NewSession(cfg, agentID, sessionID)
	if err != nil {
		return err
	}
	defer session.Close()
	session.SetDryRun(dryRun)

	m := &model{
		cfg:       cfg,
		session:   session,
		sessionID: sessionID,
		eventCh:   make(chan agent.Event, 16),
		confirmCh: make(chan confirmRequest, 1),
	}

	session.SetEventObserver(func(e agent.Event) { m.eventCh <- e })
	session.SetConfirmation(func(d governance.Decision, r *governance.EvaluateResponse) bool {
		reply := make(chan bool)
		m.confirmCh <- confirmRequest{decision: d, response: r, reply: reply}
		return <-reply
	})

	ti := textinput.New()
	ti.Placeholder = `Pidele algo a Loom... ("salir" o Ctrl+C para terminar)`
	ti.Focus()
	ti.CharLimit = 2000
	m.input = ti

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorPurple)
	m.spinner = sp

	m.viewport = viewport.New(80, 20)

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.spinner.Tick,
		waitForEvent(m.eventCh),
		waitForConfirm(m.confirmCh),
	)
}

func waitForEvent(ch chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return eventClosedMsg{}
		}
		return agentEventMsg(e)
	}
}

func waitForConfirm(ch chan confirmRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return confirmClosedMsg{}
		}
		return confirmMsg(req)
	}
}

func waitForDone(ch chan error) tea.Cmd {
	return func() tea.Msg {
		return runDoneMsg{err: <-ch}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		m.applyEvent(agent.Event(msg))
		m.viewport.SetContent(strings.Join(m.transcript, "\n"))
		m.viewport.GotoBottom()
		return m, waitForEvent(m.eventCh)

	case confirmMsg:
		req := confirmRequest(msg)
		m.pendingConfirm = &req
		return m, nil // re-issued only after the modal is answered, see handleKey

	case runDoneMsg:
		m.running = false
		m.lastErr = msg.err
		if msg.err != nil {
			m.transcript = append(m.transcript, errorLineStyle.Render("error: "+msg.err.Error()))
			m.viewport.SetContent(strings.Join(m.transcript, "\n"))
			m.viewport.GotoBottom()
		}
		m.input.Focus()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case eventClosedMsg, confirmClosedMsg:
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A pending governance confirmation takes over all keyboard input --
	// nothing else (typing a new prompt, quitting) is allowed until it's
	// answered, mirroring how `loom run` blocks on the same y/N prompt.
	if m.pendingConfirm != nil {
		switch strings.ToLower(msg.String()) {
		case "y":
			m.pendingConfirm.reply <- true
			m.pendingConfirm = nil
			return m, waitForConfirm(m.confirmCh)
		case "n", "enter", "esc":
			m.pendingConfirm.reply <- false
			m.pendingConfirm = nil
			return m, waitForConfirm(m.confirmCh)
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEnter:
		if m.running {
			return m, nil // one task at a time -- see resume/cancel roadmap note
		}
		prompt := strings.TrimSpace(m.input.Value())
		if prompt == "" {
			return m, nil
		}
		if strings.EqualFold(prompt, "salir") || strings.EqualFold(prompt, "exit") {
			return m, tea.Quit
		}
		if strings.EqualFold(prompt, "/clear") {
			m.session.ClearHistory()
			m.plan = nil
			m.transcript = append(m.transcript, routeLineStyle.Render("-- conversacion reiniciada --"))
			m.viewport.SetContent(strings.Join(m.transcript, "\n"))
			m.viewport.GotoBottom()
			m.input.SetValue("")
			return m, nil
		}
		m.input.SetValue("")
		return m, m.startRun(prompt)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) startRun(prompt string) tea.Cmd {
	m.running = true
	m.transcript = append(m.transcript, userLineStyle.Render("> "+prompt))
	m.viewport.SetContent(strings.Join(m.transcript, "\n"))
	m.viewport.GotoBottom()
	m.doneCh = make(chan error, 1)
	go func() {
		m.doneCh <- m.session.RunWithTask(context.Background(), prompt, "")
	}()
	return waitForDone(m.doneCh)
}

func (m *model) applyEvent(e agent.Event) {
	switch e.Type {
	case agent.EventRoute:
		m.lastTask, m.lastProvider, m.lastReason = e.Task, e.Provider, e.Reason
		m.transcript = append(m.transcript, routeLineStyle.Render(fmt.Sprintf("route task=%s provider=%s", e.Task, e.Provider)))
	case agent.EventText:
		m.transcript = append(m.transcript, textLineStyle.Render(e.Text))
	case agent.EventPlan:
		m.plan = e.Steps
	case agent.EventToolResult:
		m.toolCalls++
		m.lastDecision = string(e.Decision)
		m.lastRisk = e.RiskScore
		line := fmt.Sprintf("[%s] %s engine=%s risk=%.1f", e.Decision, e.Tool, e.Engine, e.RiskScore)
		m.transcript = append(m.transcript, decisionStyle(string(e.Decision)).Render(line))
	}
}

func (m *model) layout() {
	sidebarWidth := 30
	if m.width > 0 && m.width < 90 {
		sidebarWidth = 0 // too narrow for two columns -- degrade to single column
	}
	transcriptWidth := m.width - sidebarWidth - 6
	if transcriptWidth < 20 {
		transcriptWidth = m.width - 4
	}
	m.viewport.Width = transcriptWidth
	m.viewport.Height = m.height - 8
	if m.viewport.Height < 3 {
		m.viewport.Height = 3
	}
	m.input.Width = m.width - 6
}

func (m *model) View() string {
	if !m.ready {
		return "iniciando loom chat..."
	}

	header := headerStyle.Render("loom chat") + "  " + statusBarStyle.Render("sesion "+m.sessionID)
	if m.running {
		header += "  " + m.spinner.View() + " pensando..."
	}

	transcriptBox := transcriptBoxStyle.Render(m.viewport.View())
	sidebar := m.renderSidebar()

	var body string
	if m.width < 90 {
		body = transcriptBox
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, transcriptBox, sidebar)
	}

	inputLabel := "> "
	if m.running {
		inputLabel = "  "
	}
	inputBox := inputBoxStyle.Render(inputLabel + m.input.View())

	view := lipgloss.JoinVertical(lipgloss.Left, header, body, inputBox,
		statusBarStyle.Render("enter enviar · /clear reinicia la conversacion · ctrl+c salir"))

	if m.pendingConfirm != nil {
		return m.renderConfirmOverlay(view)
	}
	return view
}

func (m *model) renderSidebar() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("SESION") + "\n")
	fmt.Fprintf(&b, "tarea: %s\n", orDash(m.lastTask))
	fmt.Fprintf(&b, "provider: %s\n", orDash(m.lastProvider))
	fmt.Fprintf(&b, "acciones: %d\n", m.toolCalls)
	if m.lastDecision != "" {
		fmt.Fprintf(&b, "ultima decision: %s\n", decisionStyle(m.lastDecision).Render(fmt.Sprintf("%s (%.1f)", m.lastDecision, m.lastRisk)))
	}
	b.WriteString("\n")
	b.WriteString(planTitleStyle.Render("PLAN") + "\n")
	if len(m.plan) == 0 {
		b.WriteString(statusBarStyle.Render("(sin plan todavia)") + "\n")
	}
	for i, step := range m.plan {
		mark, style := "[ ]", planPendingStyle
		switch step.Status {
		case "in_progress":
			mark, style = "[~]", planActiveStyle
		case "done":
			mark, style = "[x]", planDoneStyle
		}
		fmt.Fprintf(&b, "%s %d. %s\n", style.Render(mark), i+1, step.Title)
	}
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("HERRAMIENTAS") + "\n")
	names := m.session.ToolNames()
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("- " + name + "\n")
	}
	return sidebarBoxStyle.Width(28).Render(b.String())
}

func (m *model) renderConfirmOverlay(base string) string {
	req := m.pendingConfirm
	var b strings.Builder
	fmt.Fprintf(&b, "DevMind: %s\n\n", decisionStyle(string(req.decision)).Render(string(req.decision)))
	fmt.Fprintf(&b, "risk_score=%.1f\n", req.response.RiskScore)
	for _, why := range req.response.Why {
		fmt.Fprintf(&b, "- %s\n", why)
	}
	b.WriteString("\ncontinuar de todos modos? [y/n]")
	overlay := modalStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, overlay)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
