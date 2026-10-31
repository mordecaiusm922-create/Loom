package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// status is the session state shown in the header: a colored dot plus a
// short label, driven by the agent's events and the last policy verdict.

type status int

const (
	statusIdle status = iota
	statusThinking
	statusDone
	statusReview
	statusBlocked
	statusError
)

// ticksPerS is the spinner's rate (spinner.Dot ticks at 10 FPS); transient
// statuses are timed in spinner ticks.
const ticksPerS = 10

func (s status) label() string {
	switch s {
	case statusThinking:
		return "investigating"
	case statusDone:
		return "verified"
	case statusReview:
		return "review required"
	case statusBlocked:
		return "blocked by policy"
	case statusError:
		return "error"
	default:
		return "ready"
	}
}

func (s status) color() lipgloss.Color {
	switch s {
	case statusThinking:
		return colorAccent
	case statusDone:
		return colorGreen
	case statusReview:
		return colorYellow
	case statusBlocked, statusError:
		return colorRed
	default:
		return colorMuted
	}
}

// indicator renders "● label" in the status color.
func (s status) indicator() string {
	return lipgloss.NewStyle().Foreground(s.color()).Bold(true).Render("● " + s.label())
}

var wordmarkRows = []string{
	"█    ▄▀▀▄ ▄▀▀▄ █▄ ▄█",
	"█    █  █ █  █ █ ▀ █",
	"█▄▄▄ ▀▄▄▀ ▀▄▄▀ █   █",
}

// wordmark renders "LOOM" in block letters in the accent color.
func wordmark() string {
	return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(strings.Join(wordmarkRows, "\n"))
}

// welcome is shown in the transcript area until the first prompt.
func welcome(width int, sessionID, context, engine string) string {
	tagline := lipgloss.NewStyle().Foreground(colorMuted).
		Render("policy-gated SRE agent: nothing runs without a verdict")
	if width < 64 {
		return headerStyle.Render("LOOM") + "\n" + tagline
	}

	muted := lipgloss.NewStyle().Foreground(colorMuted)
	value := lipgloss.NewStyle().Foreground(colorAccentSoft)
	info := muted.Render("policy engine ") + value.Render(engine) + muted.Render(" · session ") + value.Render(sessionID)
	if context != "" {
		info += muted.Render(" · context ") + value.Render(context)
	}
	tips := []string{
		muted.Render("try:"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("why is the checkout pod restarting in the payments namespace?"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("review this terraform change before applying it"),
		"",
		muted.Render("/clear resets the conversation · ctrl+c exits"),
	}
	return lipgloss.JoinVertical(lipgloss.Left, wordmark(), "", tagline, "", info, "", strings.Join(tips, "\n"))
}
