package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The mascot is an on-call owl: awake at 3 a.m., sees in the dark, and its
// eyes react to what DevMind decides. It is drawn only with full/half
// blocks (█ ▀ ▄) so it renders in the legacy Windows console font too --
// quadrant blocks and most geometric symbols are missing from Consolas.

type mood int

const (
	moodIdle mood = iota
	moodBlink
	moodThinking
	moodHappy
	moodAlert
	moodBlocked
	moodError
)

// eyes returns the two eye glyphs for a mood.
func (m mood) eyes() (string, string) {
	switch m {
	case moodBlink:
		return "-", "-"
	case moodThinking:
		return "o", "o"
	case moodHappy:
		return "^", "^"
	case moodAlert:
		return "O", "O"
	case moodBlocked:
		return ">", "<"
	case moodError:
		return "x", "x"
	default:
		return "●", "●"
	}
}

func (m mood) eyeColor() lipgloss.Color {
	switch m {
	case moodBlocked, moodError:
		return colorRed
	case moodAlert:
		return colorYellow
	case moodHappy:
		return colorGreen
	default:
		return colorAmber
	}
}

// face is the one-line owl used in the header: {●v●}
func (m mood) face() string {
	left, right := m.eyes()
	body := lipgloss.NewStyle().Foreground(colorOwl).Bold(true)
	eye := lipgloss.NewStyle().Foreground(m.eyeColor()).Bold(true)
	return body.Render("{") + eye.Render(left) + body.Render("v") + eye.Render(right) + body.Render("}")
}

// owl renders the four-line owl.
//
//	▄▀▄▄▄▀▄
//	█ ● ● █
//	█▄ v ▄█
//	 ▀▄▄▄▀
func (m mood) owl() string {
	left, right := m.eyes()
	body := lipgloss.NewStyle().Foreground(colorOwl)
	eye := lipgloss.NewStyle().Foreground(m.eyeColor()).Bold(true)
	beak := lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	lines := []string{
		body.Render("▄▀▄▄▄▀▄"),
		body.Render("█ ") + eye.Render(left) + " " + eye.Render(right) + body.Render(" █"),
		body.Render("█▄ ") + beak.Render("v") + body.Render(" ▄█"),
		body.Render(" ▀▄▄▄▀ "),
	}
	return strings.Join(lines, "\n")
}

// say is what the owl says for a mood, shown under the header.
func (m mood) say() string {
	switch m {
	case moodThinking:
		return "investigando..."
	case moodHappy:
		return "listo. todo verificado."
	case moodAlert:
		return "esto necesita tus ojos."
	case moodBlocked:
		return "DevMind lo bloqueo. no voy a insistir."
	case moodError:
		return "algo fallo. revisa el error."
	default:
		return "de guardia."
	}
}

var wordmarkRows = []string{
	"█    ▄▀▀▄ ▄▀▀▄ █▄ ▄█",
	"█    █  █ █  █ █ ▀ █",
	"█▄▄▄ ▀▄▄▀ ▀▄▄▀ █   █",
}

// wordmark renders "LOOM" in block letters with a horizontal gradient from
// violet to cyan: the colors of a thread being woven.
func wordmark() string {
	width := 0
	for _, row := range wordmarkRows {
		if n := len([]rune(row)); n > width {
			width = n
		}
	}
	var out []string
	for _, row := range wordmarkRows {
		var b strings.Builder
		for i, r := range []rune(row) {
			if r == ' ' {
				b.WriteRune(r)
				continue
			}
			b.WriteString(lipgloss.NewStyle().Foreground(gradient(i, width)).Bold(true).Render(string(r)))
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

// gradient interpolates between gradientFrom and gradientTo. lipgloss
// degrades true color to the terminal's palette on its own.
func gradient(i, width int) lipgloss.Color {
	from, to := [3]float64{167, 139, 250}, [3]float64{34, 211, 238} // #A78BFA -> #22D3EE
	t := 0.0
	if width > 1 {
		t = float64(i) / float64(width-1)
	}
	c := [3]int{}
	for k := range c {
		c[k] = int(from[k] + (to[k]-from[k])*t + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2]))
}

// welcome is shown in the transcript area until the first prompt.
func welcome(m mood, width int, sessionID, context string) string {
	tagline := lipgloss.NewStyle().Foreground(colorGray).Italic(true).
		Render("el agente de SRE que nunca aplica lo que tu politica no aprobo")
	if width < 64 {
		// Too narrow for the block letters: face + plain name.
		return m.face() + "  " + headerStyle.Render("loom") + "\n" + tagline
	}

	banner := lipgloss.JoinHorizontal(lipgloss.Center, m.owl(), "    ", wordmark())
	muted := lipgloss.NewStyle().Foreground(colorGray)
	accent := lipgloss.NewStyle().Foreground(colorCyan)
	info := []string{
		muted.Render("gobernado por ") + accent.Render("DevMind") + muted.Render(" · sesion ") + accent.Render(sessionID),
	}
	if context != "" {
		info = append(info, muted.Render("contexto ")+accent.Render(context))
	}
	tips := []string{
		muted.Render("prueba:"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("por que reinicia el pod checkout en el namespace payments?"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("revisa este cambio de terraform antes de aplicarlo"),
		"",
		muted.Render("/clear reinicia la conversacion · ctrl+c sale"),
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		banner,
		"",
		tagline,
		strings.Join(info, "\n"),
		"",
		strings.Join(tips, "\n"),
	)
}
