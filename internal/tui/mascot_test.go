package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Quadrant blocks (▖▗▘▝▙▚▛▜▟) and most geometric symbols are missing from
// the legacy Windows console font; the mascot and wordmark must only use
// glyphs that render there.
func TestMascotUsesConsoleSafeGlyphs(t *testing.T) {
	var all strings.Builder
	for _, m := range []mood{moodIdle, moodBlink, moodThinking, moodHappy, moodAlert, moodBlocked, moodError} {
		all.WriteString(ansi.Strip(m.owl()))
		all.WriteString(ansi.Strip(m.face()))
	}
	all.WriteString(ansi.Strip(wordmark()))
	for _, r := range all.String() {
		if r >= 0x2596 && r <= 0x259F {
			t.Fatalf("quadrant block %q is not console-safe", r)
		}
	}
}

func TestOwlLinesHaveEqualWidth(t *testing.T) {
	for _, m := range []mood{moodIdle, moodAlert, moodBlocked} {
		lines := strings.Split(ansi.Strip(m.owl()), "\n")
		for _, line := range lines {
			if ansi.StringWidth(line) != ansi.StringWidth(lines[0]) {
				t.Fatalf("mood %d: ragged owl %q", m, lines)
			}
		}
	}
}

func TestEyesReflectDecisions(t *testing.T) {
	if l, r := moodBlocked.eyes(); l+r != "><" {
		t.Fatalf("blocked eyes = %s%s", l, r)
	}
	if l, _ := moodAlert.eyes(); l != "O" {
		t.Fatalf("alert eyes = %s", l)
	}
}

func TestTransientMoodFadesAndIdleBlinks(t *testing.T) {
	m := &model{}
	m.setMood(moodHappy, 3)
	for i := 0; i < 3; i++ {
		m.tick()
	}
	if m.mood != moodIdle {
		t.Fatalf("mood = %d after hold, want idle", m.mood)
	}
	blinked := false
	for i := 0; i < 100; i++ {
		m.tick()
		if m.mood == moodBlink {
			blinked = true
		}
	}
	if !blinked || m.mood == moodBlocked {
		t.Fatal("idle owl never blinked")
	}
	// Non-transient moods (thinking, alert, blocked) never blink or fade.
	m.setMood(moodAlert, 0)
	for i := 0; i < 100; i++ {
		m.tick()
	}
	if m.mood != moodAlert {
		t.Fatalf("alert faded to %d", m.mood)
	}
}

func TestWelcomeDegradesOnNarrowTerminals(t *testing.T) {
	wide := ansi.Strip(welcome(moodIdle, 100, "sess-x", "prod-eks"))
	if !strings.Contains(wide, "█▄▄▄") || !strings.Contains(wide, "prod-eks") {
		t.Fatalf("wide welcome missing wordmark or context:\n%s", wide)
	}
	narrow := ansi.Strip(welcome(moodIdle, 40, "sess-x", ""))
	if strings.Contains(narrow, "█▄▄▄") {
		t.Fatal("narrow welcome must not render block letters")
	}
	t.Log("\n" + wide)
}
