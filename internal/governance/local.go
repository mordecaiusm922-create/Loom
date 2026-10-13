package governance

import (
	"context"
	"regexp"
	"strings"
)

// LocalEngine is the policy Loom uses when no DevMind token is configured,
// so anyone can try Loom in minutes without an account. It is deliberately
// conservative and coarse:
//
//   - native read-only tools run (ALLOW);
//   - every other action asks the operator first (REVIEW);
//   - an organization- or account-wide destructive change is ESCALATE;
//   - a short list of catastrophic commands is always BLOCK.
//
// It is a seatbelt, not a policy engine: it does not score risk, keep
// session history or write an audit trail outside .loom/sessions. Those,
// and verdicts fine-grained enough that safe changes do not interrupt
// you, come from DevMind.
type LocalEngine struct{}

// Name identifies the engine in operator-facing messages.
func (LocalEngine) Name() string { return "local policy" }

// readOnlyTools are Loom's native tools that never change state. Anything
// else -- the shell tool, write_file, MCP tools -- may mutate.
var readOnlyTools = map[string]bool{
	"read_file":     true,
	"k8s_get":       true,
	"k8s_describe":  true,
	"k8s_logs":      true,
	"cloud_read":    true,
	"metrics_query": true,
	"update_plan":   true,
}

// end matches what may follow a target path: whitespace, a quote, a shell
// separator, the closing bracket of a formatted tool input, or end of text.
const end = `(?:\s|["'\]\);&|]|$)`

var catastrophic = []struct {
	reason  string
	pattern *regexp.Regexp
}{
	{"recursive delete of the filesystem root or home directory",
		regexp.MustCompile(`(?i)\brm\s+(?:-\S+\s+)*-\S*r\S*\s+(?:-\S+\s+)*(?:/|/\*|~|~/|~/\*|\$home|\$\{home\})` + end)},
	{"filesystem format", regexp.MustCompile(`(?i)\bmkfs(?:\.\w+)?\b|\bformat-volume\b`)},
	{"raw write to a disk device", regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/(?:sd|hd|xvd|nvme|disk|vd)\w*`)},
	{"fork bomb", regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)},
	{"remote script piped into a shell",
		regexp.MustCompile(`(?i)\b(?:curl|wget|iwr|invoke-webrequest|irm|invoke-restmethod)\b[^\n|]*\|\s*(?:sudo\s+)?(?:ba|z|da|k)?sh\b|\|\s*(?:iex|invoke-expression)\b`)},
	{"database drop", regexp.MustCompile(`(?i)\bdrop\s+(?:database|schema)\b`)},
	{"world-writable filesystem root", regexp.MustCompile(`(?i)\bchmod\s+(?:-\S+\s+)*-R\s+(?:-\S+\s+)*0?777\s+/` + end)},
}

func catastrophicReason(payload string) string {
	for _, rule := range catastrophic {
		if rule.pattern.MatchString(payload) {
			return rule.reason
		}
	}
	return ""
}

func (LocalEngine) EvaluateAction(_ context.Context, _ string, tool, _ string, payload string, affectsProduction bool) (*EvaluateResponse, error) {
	if reason := catastrophicReason(payload); reason != "" {
		return localDecision(Block, 100, "local policy: "+reason+" is always blocked"), nil
	}
	if readOnlyTools[tool] {
		return localDecision(Allow, 0, "local policy: read-only tool"), nil
	}
	why := "local policy: this action can change state, so it needs your confirmation"
	if affectsProduction {
		why = "local policy: this action can change production, so it needs your confirmation"
	}
	return localDecision(Review, 50, why), nil
}

func (LocalEngine) EvaluateChange(_ context.Context, _ string, changeType, _ string, payload string, _ bool, blastRadius string) (*EvaluateResponse, error) {
	if reason := catastrophicReason(payload); reason != "" {
		return localDecision(Block, 100, "local policy: "+reason+" is always blocked"), nil
	}
	switch strings.ToLower(blastRadius) {
	case "org", "account":
		return localDecision(Escalate, 90, "local policy: destructive change with organization-wide blast radius"), nil
	}
	if changeType == "terraform_plan" {
		return localDecision(Allow, 0, "local policy: terraform plan does not change infrastructure"), nil
	}
	return localDecision(Review, 60, "local policy: infrastructure change ("+changeType+") needs your confirmation"), nil
}

func localDecision(d Decision, score float64, why string) *EvaluateResponse {
	return &EvaluateResponse{DecisionValue: d, RiskScore: score, Why: []string{why}}
}

// EngineName returns a short operator-facing name for an engine.
func EngineName(e Engine) string {
	if named, ok := e.(interface{ Name() string }); ok {
		return named.Name()
	}
	return "governance"
}
