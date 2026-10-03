package tools

import (
	"fmt"
	"unicode/utf8"

	"github.com/mordecaiusm922-create/loom/internal/config"
)

// truncateOutput keeps the head and the tail of an oversized tool output.
// Both ends matter for SRE output: the head of `kubectl get` / `terraform
// plan` carries the headers and first resources, the tail of logs carries
// the error that ended the process. The marker tells the model how much
// was dropped so it can ask for a narrower query (--tail, -l selector,
// -target) instead of assuming it saw everything.
func truncateOutput(out string, limit int) string {
	switch {
	case limit == 0:
		limit = config.DefaultMaxToolOutputBytes
	case limit < 0:
		return out
	}
	if len(out) <= limit {
		return out
	}
	half := limit / 2
	head := out[:runeStart(out, half)]
	tail := out[runeStart(out, len(out)-half):]
	dropped := len(out) - len(head) - len(tail)
	return fmt.Sprintf("%s\n\n[... %d bytes truncated by Loom; narrow the query (--tail, --since, a label selector, -target) to see this part ...]\n\n%s", head, dropped, tail)
}

// runeStart moves i back to the start of the UTF-8 rune containing it, so
// truncation never splits a multi-byte character.
func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
