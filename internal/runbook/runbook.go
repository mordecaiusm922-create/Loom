// Package runbook loads reusable incident-response procedures from disk so
// an SRE team can standardize "what to do when X happens" instead of
// re-deriving it under pressure during every incident. A runbook seeds
// Loom's initial prompt and its declared task hint; the model still
// reasons about the specific situation, it does not just replay steps
// blindly.
package runbook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Runbook is a named, reusable procedure. Steps are suggestions handed to
// the model as context, not an executable script -- the model still calls
// update_plan itself and adapts to what it actually finds.
type Runbook struct {
	Title    string   `json:"title"`
	TaskHint string   `json:"task_hint,omitempty"`
	Steps    []string `json:"steps"`
}

// DefaultDir is used when config.SreConfig.RunbooksDir is empty.
const DefaultDir = "runbooks"

// Load reads <dir>/<name>.json. dir defaults to DefaultDir when empty.
func Load(dir, name string) (Runbook, error) {
	if dir == "" {
		dir = DefaultDir
	}
	if name == "" {
		return Runbook{}, fmt.Errorf("nombre de runbook vacio")
	}
	path := filepath.Join(dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Runbook{}, fmt.Errorf("runbook %q no encontrado en %s (usa 'loom runbooks' para listar los disponibles)", name, dir)
		}
		return Runbook{}, err
	}
	var rb Runbook
	if err := json.Unmarshal(data, &rb); err != nil {
		return Runbook{}, fmt.Errorf("runbook %q invalido: %w", name, err)
	}
	if len(rb.Steps) == 0 {
		return Runbook{}, fmt.Errorf("runbook %q no declara 'steps'", name)
	}
	return rb, nil
}

// List returns the runbook names available in dir (without the .json
// extension), sorted. A missing directory is not an error -- it just means
// no runbooks exist yet.
func List(dir string) ([]string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// ComposePrompt merges a runbook's suggested steps with the operator's own
// framing of the situation into a single prompt for the model. extra may be
// empty when the runbook alone is enough to start ("investigate and report
// back" style tasks).
func (rb Runbook) ComposePrompt(extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Segui el runbook %q como punto de partida (adaptalo a lo que encuentres, no lo repitas ciegamente si la evidencia apunta a otro lado):\n", rb.Title)
	for i, step := range rb.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	if strings.TrimSpace(extra) != "" {
		fmt.Fprintf(&b, "\nContexto adicional de quien reporta:\n%s\n", extra)
	}
	return b.String()
}
