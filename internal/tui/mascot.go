package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The mascot is an on-call owl: awake at 3 a.m., sees in the dark, and its
// eyes react to what DevMind decides.
//
// It is pixel art. Every terminal cell shows two vertical pixels with the
// upper half block "▀": foreground = top pixel, background = bottom pixel.
// Terminal cells are about twice as tall as they are wide, so the pixels
// come out square. "▀"/"▄" exist in every console font, including the
// legacy Windows one, and color comes from VT sequences that Windows 10+
// conhost supports.

type mood int

const (
	moodIdle mood = iota
	moodThinking
	moodHappy
	moodAlert
	moodBlocked
	moodError
)

// owlPalette maps sprite characters to colors. '.' is transparent.
var owlPalette = map[byte]lipgloss.Color{
	'a': "#8B5CF6", // body
	'b': "#6D28D9", // wings
	'c': "#EDE9FE", // belly
	'w': "#FFFFFF", // eye white
	'k': "#1E1B4B", // pupil
	'o': "#F59E0B", // beak and feet
	'r': "#EF4444", // angry pupil
	'y': "#FBBF24", // alert eye
}

// owlBase is 16x14 pixels: 16 columns by 7 terminal rows.
var owlBase = []string{
	"..a..........a..",
	"..aa........aa..",
	"..aaaaaaaaaaaa..",
	".aaaaaaaaaaaaaa.",
	".aawwaaaaaawwaa.",
	".awkkwaaaawkkwa.",
	".awkkwaooawkkwa.",
	".bawwaaaaaawwab.",
	"bbaccccccccccabb",
	"bbaccccccccccabb",
	".baccccccccccab.",
	"..aaccccccccaa..",
	"...aaaaaaaaaa...",
	"....oo....oo....",
}

// owlFrames are owlBase with a few rows replaced.
var owlFrames = map[string][]string{
	"idle":       owlBase,
	"blink":      owlWith(4, ".aaaaaaaaaaaaaa.", ".aaaaaaaaaaaaaa.", ".akkkkaooakkkka.", ".baaaaaaaaaaaab."),
	"look_left":  owlWith(5, ".akkwwaaaakkwwa.", ".akkwwaooakkwwa."),
	"look_right": owlWith(5, ".awwkkaaaawwkka.", ".awwkkaooawwkka."),
	"happy":      owlWith(4, ".aaaaaaaaaaaaaa.", ".aakkaaaaaakkaa.", ".akaakaooakaaka.", ".baaaaaaaaaaaab."),
	"alert":      owlWith(4, ".aayyaaaaaayyaa.", ".aykkyaaaaykkya.", ".aykkyaooaykkya.", ".bayyaaaaaayyab."),
	"blocked":    owlWith(3, ".akkaaaaaaaakka.", ".aaakkaaaakkaaa.", ".awrrwaaaawrrwa.", ".awrrwaooawrrwa."),
	"dizzy":      owlWith(4, ".akaakaaaakaaka.", ".aakkaaaaaakkaa.", ".akaakaooakaaka.", ".baaaaaaaaaaaab."),
	"flap":       owlWith(7, "bbawwaaaaaawwabb", "b.acccccccccca.b", "..acccccccccca..", "..acccccccccca.."),
}

func owlWith(from int, rows ...string) []string {
	out := append([]string(nil), owlBase...)
	copy(out[from:], rows)
	return out
}

// Canvas: one transparent pixel of margin on every side, so a shake (dx)
// or a bounce (dy) never changes the rendered size and the layout never
// jumps.
const (
	owlW       = 16
	owlH       = 14
	canvasW    = owlW + 2
	canvasH    = owlH + 2
	ticksPerS  = 10 // spinner.Dot ticks at 10 FPS; the animation clock rides on it
	idleCycleT = 6 * ticksPerS
)

// pose picks the frame and offset for a mood, t ticks after the mood began.
func (m mood) pose(t int) (frame string, dx, dy int) {
	switch m {
	case moodThinking:
		// Looks around, flapping between glances, with a small bob.
		frames := []string{"look_left", "flap", "look_right", "flap"}
		frame = frames[(t/3)%len(frames)]
		if (t/3)%2 == 1 {
			dy = -1
		}
	case moodAlert:
		frame = "alert"
		if t < 7 { // a short startled shake, then hold still
			dx = 1 - 2*(t%2)
		}
	case moodBlocked:
		frame = "blocked"
	case moodHappy:
		frame = "happy"
		if (t/3)%2 == 1 {
			dy = -1
		}
	case moodError:
		frame = "dizzy"
	default:
		// Idle: mostly still, blinks, glances left and right now and then.
		switch c := t % idleCycleT; {
		case c == 20 || c == 57:
			frame = "blink"
		case c >= 33 && c < 40:
			frame = "look_left"
		case c >= 40 && c < 47:
			frame = "look_right"
		default:
			frame = "idle"
		}
	}
	return frame, dx, dy
}

// owl renders the animated owl for a mood at t ticks into that mood.
func (m mood) owl(t int) string {
	frame, dx, dy := m.pose(t)
	return renderPixels(owlFrames[frame], dx+1, dy+1)
}

// renderPixels draws sprite rows onto a canvasW x canvasH transparent
// canvas at (ox, oy) and folds pixel rows in pairs into "▀" cells.
func renderPixels(rows []string, ox, oy int) string {
	canvas := make([][]byte, canvasH)
	for y := range canvas {
		canvas[y] = []byte(strings.Repeat(".", canvasW))
	}
	for y, row := range rows {
		for x := 0; x < len(row); x++ {
			cx, cy := x+ox, y+oy
			if cx >= 0 && cx < canvasW && cy >= 0 && cy < canvasH {
				canvas[cy][cx] = row[x]
			}
		}
	}
	var b strings.Builder
	for y := 0; y < canvasH; y += 2 {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < canvasW; x++ {
			top, topOK := owlPalette[canvas[y][x]]
			bottom, bottomOK := owlPalette[canvas[y+1][x]]
			switch {
			case topOK && bottomOK:
				b.WriteString(lipgloss.NewStyle().Foreground(top).Background(bottom).Render("▀"))
			case topOK:
				b.WriteString(lipgloss.NewStyle().Foreground(top).Render("▀"))
			case bottomOK:
				b.WriteString(lipgloss.NewStyle().Foreground(bottom).Render("▄"))
			default:
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

// face is a one-line owl for terminals too narrow for the pixel owl.
func (m mood) face() string {
	eyes := map[mood][2]string{
		moodThinking: {"o", "o"}, moodHappy: {"^", "^"}, moodAlert: {"O", "O"},
		moodBlocked: {">", "<"}, moodError: {"x", "x"},
	}
	e, ok := eyes[m]
	if !ok {
		e = [2]string{"●", "●"}
	}
	body := lipgloss.NewStyle().Foreground(owlPalette['a']).Bold(true)
	eye := lipgloss.NewStyle().Foreground(m.eyeColor()).Bold(true)
	return body.Render("{") + eye.Render(e[0]) + body.Render("v") + eye.Render(e[1]) + body.Render("}")
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

// say is what the owl says for a mood.
func (m mood) say() string {
	switch m {
	case moodThinking:
		return "investigando..."
	case moodHappy:
		return "listo. todo verificado."
	case moodAlert:
		return "esto necesita tus ojos."
	case moodBlocked:
		return "DevMind lo bloqueo."
	case moodError:
		return "algo fallo."
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

// gradient interpolates #A78BFA -> #22D3EE; lipgloss degrades true color
// to the terminal's palette on its own.
func gradient(i, width int) lipgloss.Color {
	from, to := [3]float64{167, 139, 250}, [3]float64{34, 211, 238}
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
func welcome(m mood, t, width int, sessionID, context string) string {
	tagline := lipgloss.NewStyle().Foreground(colorGray).Italic(true).
		Render("el agente de SRE que nunca aplica lo que tu politica no aprobo")
	if width < 64 {
		return m.owl(t) + "\n\n" + headerStyle.Render("loom") + "\n" + tagline
	}

	title := lipgloss.JoinVertical(lipgloss.Left, wordmark(), "", tagline)
	banner := lipgloss.JoinHorizontal(lipgloss.Center, m.owl(t), "   ", title)
	muted := lipgloss.NewStyle().Foreground(colorGray)
	accent := lipgloss.NewStyle().Foreground(colorCyan)
	info := muted.Render("gobernado por ") + accent.Render("DevMind") + muted.Render(" · sesion ") + accent.Render(sessionID)
	if context != "" {
		info += muted.Render(" · contexto ") + accent.Render(context)
	}
	tips := []string{
		muted.Render("prueba:"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("por que reinicia el pod checkout en el namespace payments?"),
		"  " + lipgloss.NewStyle().Foreground(colorWhite).Render("revisa este cambio de terraform antes de aplicarlo"),
		"",
		muted.Render("/clear reinicia la conversacion · ctrl+c sale"),
	}
	return lipgloss.JoinVertical(lipgloss.Left, banner, "", info, "", strings.Join(tips, "\n"))
}
