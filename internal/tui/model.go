// Package tui implements `loom chat`, an interactive Bubble Tea front-end
// over the exact same agent.Session used by `loom run`. It is a
// presentation layer, not a second agent implementation: it subscribes to
// the same Event stream and the same governance confirmation hook that the
// plain-text CLI already uses.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mordecaiusm922-create/loom/internal/agent"
	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
	"github.com/mordecaiusm922-create/loom/internal/tools"
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

	// header status
	status      status
	statusTicks int // spinner ticks left before a transient status fades to idle
	envName     string
	envTier     string
}

// statusHold is how long a transient status (verified, error) stays before
// it goes back to idle: ~3s at the spinner's 10 ticks per second.
const statusHold = 3 * ticksPerS

// setStatus changes the header status. hold > 0 makes it transient: it
// fades back to idle after hold ticks.
func (m *model) setStatus(next status, hold int) {
	m.status, m.statusTicks = next, hold
}

// tick fades transient statuses.
func (m *model) tick() {
	if m.statusTicks > 0 {
		m.statusTicks--
		if m.statusTicks == 0 {
			m.setStatus(statusIdle, 0)
		}
	}
}

// engineName is the policy engine this session uses, for display.
func (m *model) engineName() string {
	if !m.cfg.Governance.Enabled {
		return "disabled"
	}
	return m.cfg.Governance.ResolvedEngine()
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
	// Environment badge: resolved from the configured kube context exactly
	// the way governance resolves it for every native k8s_* call.
	if cfg.Sre.KubeContext != "" {
		env := tools.ResolveEnvironment("k8s_get", map[string]any{}, cfg.Sre)
		m.envName, m.envTier = cfg.Sre.KubeContext, env.Tier
	}

	session.SetEventObserver(func(e agent.Event) { m.eventCh <- e })
	session.SetConfirmation(func(d governance.Decision, r *governance.EvaluateResponse) bool {
		reply := make(chan bool)
		m.confirmCh <- confirmRequest{decision: d, response: r, reply: reply}
		return <-reply
	})

	ti := textinput.New()
	ti.Placeholder = "what should we investigate?"
	ti.Prompt = ""
	ti.Focus()
	ti.CharLimit = 2000
	m.input = ti

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)
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
		m.viewport.SetContent(m.renderTranscript()) // re-wrap to the new width
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		m.applyEvent(agent.Event(msg))
		m.viewport.SetContent(m.renderTranscript())
		m.viewport.GotoBottom()
		return m, waitForEvent(m.eventCh)

	case confirmMsg:
		req := confirmRequest(msg)
		m.pendingConfirm = &req
		m.setStatus(statusReview, 0)
		return m, nil // re-issued only after the modal is answered, see handleKey

	case runDoneMsg:
		m.running = false
		m.lastErr = msg.err
		if msg.err != nil {
			m.setStatus(statusError, statusHold*2)
			m.transcript = append(m.transcript, errorLineStyle.Render("  x "+msg.err.Error()))
			m.viewport.SetContent(m.renderTranscript())
			m.viewport.GotoBottom()
		} else {
			m.setStatus(statusDone, statusHold)
		}
		m.input.Focus()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.tick()
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
			m.setStatus(statusThinking, 0)
			return m, waitForConfirm(m.confirmCh)
		case "n", "enter", "esc":
			m.pendingConfirm.reply <- false
			m.pendingConfirm = nil
			m.setStatus(statusThinking, 0)
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
			m.transcript = append(m.transcript, routeLineStyle.Render("-- conversation reset --"))
			m.viewport.SetContent(m.renderTranscript())
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
	m.setStatus(statusThinking, 0)
	if len(m.transcript) > 0 {
		m.transcript = append(m.transcript, "")
	}
	m.transcript = append(m.transcript, promptMarkStyle.Render("› ")+userLineStyle.Render(prompt))
	m.viewport.SetContent(m.renderTranscript())
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
		m.transcript = append(m.transcript, routeLineStyle.Render(fmt.Sprintf("  %s · %s", e.Task, e.Provider)))
	case agent.EventText:
		m.transcript = append(m.transcript, bulletStyle.Render("● ")+textLineStyle.Render(e.Text))
	case agent.EventPlan:
		m.plan = e.Steps
	case agent.EventToolStart:
		if m.pendingConfirm == nil {
			m.setStatus(statusThinking, 0)
		}
	case agent.EventToolResult:
		m.toolCalls++
		m.lastDecision = string(e.Decision)
		m.lastRisk = e.RiskScore
		if e.Decision == governance.Block {
			m.setStatus(statusBlocked, 0)
		}
		decision := string(e.Decision)
		if decision == "" {
			decision = e.Engine // dry-run: no governance decision was requested
		}
		line := "  │ " + decisionStyle(string(e.Decision)).Render(fmt.Sprintf("%-8s", decision)) +
			toolNameStyle.Render(e.Tool) + routeLineStyle.Render(fmt.Sprintf("  risk %.1f · %s", e.RiskScore, orDash(e.Outcome)))
		m.transcript = append(m.transcript, line)
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
	// box border (2) + padding (2) + "› " label (2) + cursor (1): anything
	// wider wraps the input onto a second line.
	m.input.Width = m.width - 9
}

func (m *model) View() string {
	if !m.ready {
		return "starting loom chat..."
	}

	header := " " + headerStyle.Render("LOOM")
	if badge := m.envBadge(); badge != "" {
		header += "  " + badge
	}
	header += "  " + m.status.indicator()
	if m.running {
		header += " " + m.spinner.View()
	}

	content := m.viewport.View()
	if len(m.transcript) == 0 {
		content = lipgloss.Place(m.viewport.Width, m.viewport.Height, lipgloss.Center, lipgloss.Center,
			welcome(m.viewport.Width, m.sessionID, m.envName, m.engineName()))
	}
	transcriptBox := transcriptBoxStyle.Render(content)
	sidebar := m.renderSidebar()

	var body string
	if m.width < 90 {
		body = transcriptBox
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, transcriptBox, sidebar)
	}

	inputLabel := promptMarkStyle.Render("› ")
	if m.running {
		inputLabel = "  "
	}
	inputBox := inputBoxStyle.Width(m.width - 2).Render(inputLabel + m.input.View())

	view := lipgloss.JoinVertical(lipgloss.Left, header, body, inputBox,
		statusBarStyle.Render(" enter send · /clear reset · ctrl+c quit · session "+m.sessionID))

	if m.pendingConfirm != nil {
		return m.renderConfirmOverlay(view)
	}
	return view
}

func (m *model) renderSidebar() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("SESSION") + "\n")
	fmt.Fprintf(&b, "engine: %s\n", m.engineName())
	fmt.Fprintf(&b, "task: %s\n", orDash(m.lastTask))
	fmt.Fprintf(&b, "provider: %s\n", orDash(m.lastProvider))
	fmt.Fprintf(&b, "actions: %d\n", m.toolCalls)
	if m.lastDecision != "" {
		fmt.Fprintf(&b, "last decision: %s\n", decisionStyle(m.lastDecision).Render(fmt.Sprintf("%s (%.1f)", m.lastDecision, m.lastRisk)))
	}
	b.WriteString("\n")
	b.WriteString(planTitleStyle.Render("PLAN") + "\n")
	if len(m.plan) == 0 {
		b.WriteString(statusBarStyle.Render("(no plan yet)") + "\n")
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
	b.WriteString("\n" + statusBarStyle.Render(fmt.Sprintf("%d governed tools", len(m.session.ToolNames()))))
	// Same height as the transcript box: an unbounded sidebar (a long plan)
	// used to push the whole layout taller than the terminal.
	// Wrap first (sidebar content is 26 wide), then cut to the box height.
	wrapped := lipgloss.NewStyle().Width(26).Render(strings.TrimRight(b.String(), "\n"))
	lines := strings.Split(wrapped, "\n")
	if h := m.viewport.Height; h > 0 && len(lines) > h {
		lines = append(lines[:h-1], statusBarStyle.Render("..."))
	}
	return sidebarBoxStyle.Width(28).Height(m.viewport.Height).Render(strings.Join(lines, "\n"))
}

func (m *model) renderConfirmOverlay(base string) string {
	req := m.pendingConfirm
	var b strings.Builder
	fmt.Fprintf(&b, "Policy: %s   ", decisionStyle(string(req.decision)).Render(string(req.decision)))
	b.WriteString(routeLineStyle.Render(fmt.Sprintf("risk %.1f", req.response.RiskScore)))
	if req.response.AuditID != "" {
		b.WriteString(routeLineStyle.Render(" · audit " + req.response.AuditID))
	}
	b.WriteString("\n\n")
	for _, why := range req.response.Why {
		fmt.Fprintf(&b, "• %s\n", why)
	}
	b.WriteString("\n" + lipgloss.NewStyle().Bold(true).Render("continue anyway?") + "  " +
		decisionStyle("ALLOW").Render("[y] yes") + "  " + decisionStyle("BLOCK").Render("[n] no"))
	overlay := modalStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, overlay)
}

// envBadge is the always-visible environment tag: PROD in red, so nobody
// forgets which cluster the agent is pointed at.
func (m *model) envBadge() string {
	if m.envName == "" {
		return ""
	}
	switch m.envTier {
	case config.TierProd:
		return envProdStyle.Render("PROD " + m.envName)
	case config.TierUnknown:
		return envUnkStyle.Render("? " + m.envName)
	default:
		return envOtherStyle.Render(strings.ToUpper(m.envTier) + " " + m.envName)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderTranscript wraps every transcript line to the viewport width. The
// viewport itself does not wrap: without this, long model answers were cut
// off at the right edge instead of continuing on the next line.
func (m *model) renderTranscript() string {
	width := m.viewport.Width
	if width < 20 {
		width = 20
	}
	wrap := lipgloss.NewStyle().Width(width)
	lines := make([]string, len(m.transcript))
	for i, line := range m.transcript {
		lines[i] = wrap.Render(line)
	}
	return strings.Join(lines, "\n")
}
