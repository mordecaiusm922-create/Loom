package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewSessionID returns sess-<UTC timestamp>-<8 random hex chars>, e.g.
// sess-20260928T153045Z-9f2c01ab.
//
// It replaces sess-<PID>: PIDs repeat across machines and reboots (and are
// tiny in containers, where Loom is often PID 1), so two unrelated
// incidents appended into the same .loom/sessions/<id>.jsonl timeline and a
// --resume could pick up someone else's session. The timestamp keeps IDs
// sortable in `ls`; the random suffix makes them unique.
func NewSessionID() string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand does not fail on supported platforms; nanoseconds
		// still keep the ID unique enough for a local timeline.
		return fmt.Sprintf("sess-%s-%08x", time.Now().UTC().Format("20060102T150405Z"), time.Now().UnixNano()&0xffffffff)
	}
	return fmt.Sprintf("sess-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix))
}
