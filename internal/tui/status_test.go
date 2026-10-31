package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mordecaiusm922-create/loom/internal/agent"
	"github.com/mordecaiusm922-create/loom/internal/governance"

	"github.com/charmbracelet/x/ansi"
)

var allStatuses = []status{statusIdle, statusThinking, statusDone, statusReview, statusBlocked, statusError}

func TestEveryStatusHasALabelAndADot(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range allStatuses {
		got := ansi.Strip(s.indicator())
		if !strings.HasPrefix(got, "● ") || got != "● "+s.label() {
			t.Fatalf("status %d renders %q", s, got)
		}
		if seen[s.label()] {
			t.Fatalf("label %q is used twice", s.label())
		}
		seen[s.label()] = true
	}
}

// Verdict colors carry meaning: blocked and error are red, review is amber,
// verified is green.
func TestVerdictStatusesUseVerdictColors(t *testing.T) {
	cases := map[status]lipgloss.Color{statusBlocked: colorRed, statusError: colorRed, statusReview: colorYellow, statusDone: colorGreen}
	for s, want := range cases {
		if got := s.color(); got != want {
			t.Fatalf("status %q color = %s, want %s", s.label(), got, want)
		}
	}
}

func TestTransientStatusFadesToIdle(t *testing.T) {
	m := &model{}
	m.setStatus(statusDone, 3)
	for i := 0; i < 2; i++ {
		m.tick()
	}
	if m.status != statusDone {
		t.Fatalf("status faded early: %q", m.status.label())
	}
	m.tick()
	if m.status != statusIdle {
		t.Fatalf("status = %q after hold, want ready", m.status.label())
	}
	// A status set without a hold stays.
	m.setStatus(statusBlocked, 0)
	for i := 0; i < 100; i++ {
		m.tick()
	}
	if m.status != statusBlocked {
		t.Fatalf("blocked status faded: %q", m.status.label())
	}
}

func TestWelcomeDegradesOnNarrowTerminals(t *testing.T) {
	wide := ansi.Strip(welcome(100, "sess-x", "prod-eks", "local"))
	for _, want := range []string{"█▄▄▄", "prod-eks", "policy engine local"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide welcome missing %q:\n%s", want, wide)
		}
	}
	narrow := ansi.Strip(welcome(40, "sess-x", "", "local"))
	if strings.Contains(narrow, "█▄▄▄") {
		t.Fatal("narrow welcome must not render block letters")
	}
}

// Once the conversation starts, the screen is text and box drawing only:
// no pixel art anywhere, including the review modal.
func TestConversationScreensHaveNoPixelArt(t *testing.T) {
	m := newScreenModel(t)
	m.startRunForScreenshot("why is checkout restarting?")
	m.refreshTranscript()
	m.setStatus(statusReview, 0)
	m.pendingConfirm = &confirmRequest{decision: governance.Review, response: &governance.EvaluateResponse{
		DecisionValue: governance.Review, RiskScore: 6.5, Why: []string{"change on production"},
	}}
	for name, view := range map[string]string{"modal": m.View(), "sidebar": m.renderSidebar()} {
		if strings.ContainsAny(ansi.Strip(view), "▀▄") {
			t.Fatalf("%s view contains pixel art:\n%s", name, ansi.Strip(view))
		}
	}
	if !strings.Contains(ansi.Strip(m.View()), "Policy: REVIEW") {
		t.Fatal("review modal must name the policy verdict")
	}
	if !strings.Contains(ansi.Strip(m.renderSidebar()), "engine: disabled") {
		t.Fatal("sidebar must show the policy engine")
	}
}

// Regression: the viewport does not wrap, so long answers were cut off at
// the right edge. Every rendered line must fit the transcript width.
func TestTranscriptWrapsLongLines(t *testing.T) {
	m := &model{}
	m.viewport.Width = 30
	m.transcript = []string{strings.Repeat("timeout ", 20)}
	for _, line := range strings.Split(m.renderTranscript(), "\n") {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line width %d > 30: %q", w, line)
		}
	}
	if !strings.Contains(ansi.Strip(m.renderTranscript()), "timeout") || strings.Count(m.renderTranscript(), "\n") < 4 {
		t.Fatal("long line was not wrapped onto several lines")
	}
}

// Regression: a long plan made the sidebar taller than the transcript box
// and the input wrapped to two lines, so the layout overflowed the screen.
func TestLayoutFitsTheTerminal(t *testing.T) {
	m := newScreenModel(t)
	for i := 0; i < 30; i++ {
		m.plan = append(m.plan, agent.PlanStep{Title: "a step with a title long enough to wrap", Status: "pending"})
	}
	m.transcript = []string{"x"}
	if h := lipgloss.Height(m.View()); h > 34 {
		t.Fatalf("view is %d lines tall in a 34-line terminal", h)
	}
}
