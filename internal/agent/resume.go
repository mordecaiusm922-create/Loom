package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResumeContext turns a persisted audit trail into small, model-ready context.
// It deliberately extracts decisions and outcomes only: session logs never
// contain raw action payloads, and replaying the entire JSONL would waste the
// new model context window.
func ResumeContext(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" || filepath.Base(sessionID) != sessionID || strings.Contains(sessionID, ".") {
		return "", fmt.Errorf("session-id invalido")
	}
	path := filepath.Join(".loom", "sessions", sessionID+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("abriendo sesion %q: %w", sessionID, err)
	}
	defer file.Close()

	var tools []string
	var summaries []string
	scanner := bufio.NewScanner(file)
	// A single JSON record can include a server-provided reason, so leave
	// enough headroom while preserving a bounded parser.
	scanner.Buffer(make([]byte, 4*1024), 1024*1024)
	for scanner.Scan() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue // a partial final line must not block an incident handoff
		}
		var eventType string
		_ = json.Unmarshal(raw["event_type"], &eventType)
		if eventType == "session_summary" {
			var summary sessionSummaryRecord
			if json.Unmarshal(scanner.Bytes(), &summary) == nil {
				summaries = append(summaries, fmt.Sprintf("uso: %d tokens entrada, %d salida; costo estimado USD %.6f; duracion %d ms", summary.InputTokens, summary.OutputTokens, summary.EstimatedCostUSD, summary.DurationMS))
			}
			continue
		}
		var event struct {
			Tool     string `json:"Tool"`
			Decision string `json:"Decision"`
			Outcome  string `json:"Outcome"`
			Executed bool   `json:"Executed"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Tool == "" {
			continue
		}
		execution := "no ejecutada"
		if event.Executed {
			execution = "ejecutada"
		}
		tools = append(tools, fmt.Sprintf("%s: decision=%s, outcome=%s, %s", event.Tool, orUnknown(event.Decision), orUnknown(event.Outcome), execution))
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("leyendo sesion %q: %w", sessionID, err)
	}
	if len(tools) == 0 && len(summaries) == 0 {
		return "", fmt.Errorf("la sesion %q no contiene eventos utilizables", sessionID)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Resumen de la sesion anterior %s:\n", sessionID)
	if len(tools) > 0 {
		out.WriteString("Herramientas y decisiones:\n")
		for _, item := range tools {
			out.WriteString("- " + item + "\n")
		}
	}
	for _, summary := range summaries {
		out.WriteString("- " + summary + "\n")
	}
	out.WriteString("Usa este resumen como contexto; valida el estado actual antes de hacer cambios.")
	return out.String(), nil
}

func orUnknown(value string) string {
	if value == "" {
		return "desconocido"
	}
	return value
}
