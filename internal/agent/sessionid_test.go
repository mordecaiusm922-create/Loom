package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var sessionIDShape = regexp.MustCompile(`^sess-\d{8}T\d{6}Z-[0-9a-f]{8}$`)

func TestNewSessionIDIsUniqueAndResumable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewSessionID()
		if !sessionIDShape.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, sessionIDShape)
		}
		if seen[id] {
			t.Fatalf("duplicate session id %q", id)
		}
		seen[id] = true
	}
	// ResumeContext rejects IDs with dots or path separators; a generated
	// ID must always be resumable.
	chdir(t, t.TempDir())
	id := NewSessionID()
	if err := os.MkdirAll(filepath.Join(".loom", "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".loom", "sessions", id+".jsonl"), []byte(`{"Tool":"k8s_get","Decision":"ALLOW","Outcome":"completed"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeContext(id); err != nil {
		t.Fatalf("ResumeContext(%q): %v", id, err)
	}
}

// Regression: with PID-based IDs, openAuditFile appended to whatever file
// already had that name, merging unrelated incidents into one timeline.
func TestOpenAuditFileNeverAppendsToAnotherSessionsTimeline(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(".loom", "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".loom", "sessions", "sess-1234.jsonl")
	previous := []byte(`{"Tool":"powershell","Outcome":"completed"}` + "\n")
	if err := os.WriteFile(path, previous, 0644); err != nil {
		t.Fatal(err)
	}
	if file := openAuditFile("sess-1234"); file != nil {
		file.Close()
		t.Fatal("openAuditFile opened an existing timeline for writing")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(previous) {
		t.Fatal("existing timeline was modified")
	}
}
