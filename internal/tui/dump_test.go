package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestDumpOwlANSI writes the real terminal output of every mood to the file
// named by LOOM_DUMP_OWL, for visual review. Skipped otherwise.
func TestDumpOwlANSI(t *testing.T) {
	path := os.Getenv("LOOM_DUMP_OWL")
	if path == "" {
		t.Skip("set LOOM_DUMP_OWL to dump the owl")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	var b strings.Builder
	for _, m := range allMoods {
		for _, tick := range []int{0, 3, 20, 35, 42} {
			b.WriteString(m.owl(tick))
			b.WriteString("\n\x00\n")
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
}
