package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var allMoods = []mood{moodIdle, moodThinking, moodHappy, moodAlert, moodBlocked, moodError}

// Every frame must be a full 16x14 grid using only palette characters, or
// the owl renders torn.
func TestOwlFramesAreWellFormed(t *testing.T) {
	for name, rows := range owlFrames {
		if len(rows) != owlH {
			t.Fatalf("frame %s has %d rows, want %d", name, len(rows), owlH)
		}
		for i, row := range rows {
			if len(row) != owlW {
				t.Fatalf("frame %s row %d is %d wide, want %d: %q", name, i, len(row), owlW, row)
			}
			for _, ch := range []byte(row) {
				if _, ok := owlPalette[ch]; !ok && ch != '.' {
					t.Fatalf("frame %s row %d uses unknown pixel %q", name, i, ch)
				}
			}
		}
	}
}

// Every pose of every mood must name a real frame.
func TestEveryPoseHasAFrame(t *testing.T) {
	for _, m := range allMoods {
		for tick := 0; tick < 2*idleCycleT; tick++ {
			frame, _, _ := m.pose(tick)
			if _, ok := owlFrames[frame]; !ok {
				t.Fatalf("mood %d at t=%d wants missing frame %q", m, tick, frame)
			}
		}
	}
}

// The rendered owl must have a constant size across every frame and
// offset, or the layout jumps while it animates.
func TestOwlRenderHasConstantSize(t *testing.T) {
	for _, m := range allMoods {
		for tick := 0; tick < idleCycleT; tick++ {
			lines := strings.Split(ansi.Strip(m.owl(tick)), "\n")
			if len(lines) != canvasH/2 {
				t.Fatalf("mood %d t=%d: %d lines, want %d", m, tick, len(lines), canvasH/2)
			}
			for _, line := range lines {
				if w := ansi.StringWidth(line); w != canvasW {
					t.Fatalf("mood %d t=%d: line width %d, want %d", m, tick, w, canvasW)
				}
			}
		}
	}
}

// Only half blocks and spaces: they exist in every console font, including
// the legacy Windows one.
func TestOwlUsesOnlyHalfBlocks(t *testing.T) {
	for _, m := range allMoods {
		for _, r := range ansi.Strip(m.owl(0)) {
			if r != '▀' && r != '▄' && r != ' ' && r != '\n' {
				t.Fatalf("mood %d renders %q", m, r)
			}
		}
	}
}

func TestIdleOwlBlinksAndLooksAround(t *testing.T) {
	seen := map[string]bool{}
	for tick := 0; tick < idleCycleT; tick++ {
		frame, _, _ := moodIdle.pose(tick)
		seen[frame] = true
	}
	for _, want := range []string{"idle", "blink", "look_left", "look_right"} {
		if !seen[want] {
			t.Fatalf("idle cycle never shows %s", want)
		}
	}
}

func TestDecisionMoodsShowTheirFrames(t *testing.T) {
	cases := map[mood]string{moodAlert: "alert", moodBlocked: "blocked", moodHappy: "happy", moodError: "dizzy"}
	for m, want := range cases {
		if frame, _, _ := m.pose(10); frame != want {
			t.Fatalf("mood %d frame = %s, want %s", m, frame, want)
		}
	}
}

func TestTransientMoodFadesAndRestartsAnimation(t *testing.T) {
	m := &model{}
	m.ticks = 100
	m.setMood(moodHappy, 3)
	if m.animT() != 0 {
		t.Fatalf("animT = %d right after setMood, want 0", m.animT())
	}
	for i := 0; i < 3; i++ {
		m.tick()
	}
	if m.mood != moodIdle || m.animT() != 0 {
		t.Fatalf("mood=%d animT=%d after hold, want idle restarting at 0", m.mood, m.animT())
	}
	// Re-setting the same mood keeps the running animation.
	m.setMood(moodThinking, 0)
	m.tick()
	m.tick()
	m.setMood(moodThinking, 0)
	if m.animT() != 2 {
		t.Fatalf("animT = %d, want 2: same mood must not restart", m.animT())
	}
}

func TestWelcomeDegradesOnNarrowTerminals(t *testing.T) {
	wide := ansi.Strip(welcome(moodIdle, 0, 100, "sess-x", "prod-eks"))
	if !strings.Contains(wide, "█▄▄▄") || !strings.Contains(wide, "prod-eks") {
		t.Fatalf("wide welcome missing wordmark or context:\n%s", wide)
	}
	narrow := ansi.Strip(welcome(moodIdle, 0, 40, "sess-x", ""))
	if strings.Contains(narrow, "█▄▄▄") {
		t.Fatal("narrow welcome must not render block letters")
	}
}
