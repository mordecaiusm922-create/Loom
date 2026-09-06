package agent

import (
	"fmt"
	"os"
	"time"

	"loom/internal/governance"
)

// EventType identifies what kind of thing happened during a run. Both the
// plain-text CLI (loom run) and the TUI (loom chat) consume the same event
// stream -- they just render it differently. This is what makes the TUI a
// presentation layer on top of the existing, already-tested loop, not a
// second implementation of it.
type EventType string

const (
	EventRoute          EventType = "route"           // provider/task resolved for this request
	EventText           EventType = "text"            // the model produced assistant-visible text
	EventPlan           EventType = "plan"            // the model declared/updated its step plan
	EventToolStart      EventType = "tool_start"      // a tool call was requested, before governance runs
	EventToolResult     EventType = "tool_result"     // governance decided + tool ran (or didn't)
	EventSessionSummary EventType = "session_summary" // usage/cost/latency for this run
	EventDone           EventType = "done"            // the run finished successfully
	EventError          EventType = "error"           // the run stopped on an error
)

// PlanStep is one line of the model's declared plan (see the update_plan
// tool). Status is one of "pending", "in_progress", "done" -- Loom does not
// enforce the model updates these accurately, it just surfaces what the
// model reports.
type PlanStep struct {
	Title  string
	Status string
}

type Event struct {
	Type EventType

	// EventRoute
	Task     string
	Provider string
	Reason   string

	// EventText
	Text string

	// EventToolStart / EventToolResult
	Tool      string
	Engine    string // "policy" or "infra"
	Decision  governance.Decision
	RiskScore float64
	Executed  bool
	Outcome   string

	// EventSessionSummary
	InputTokens      int
	OutputTokens     int
	EstimatedCostUSD float64
	Duration         time.Duration

	// EventPlan
	Steps []PlanStep

	// EventError
	Err error
}

// defaultObserver reproduces exactly what `loom run` printed before events
// existed -- the CLI's behavior does not change when this package gains an
// event stream.
func defaultObserver(sessionID string) func(Event) {
	return func(e Event) {
		switch e.Type {
		case EventRoute:
			fmtRoute(sessionID, e)
		case EventText:
			fmtText(e)
		case EventPlan:
			fmtPlan(e)
		case EventToolResult:
			fmtToolResult(sessionID, e)
		case EventSessionSummary:
			fmtSessionSummary(sessionID, e)
		}
	}
}

func fmtSessionSummary(sessionID string, e Event) {
	fmt.Fprintf(os.Stderr, "[loom] session input_tokens=%d output_tokens=%d estimated_cost_usd=%.6f duration=%s session=%s\n",
		e.InputTokens, e.OutputTokens, e.EstimatedCostUSD, e.Duration.Round(time.Millisecond), sessionID)
}

func fmtRoute(sessionID string, e Event) {
	fmt.Fprintf(os.Stderr, "[loom] route task=%s provider=%s reason=%s session=%s\n", e.Task, e.Provider, e.Reason, sessionID)
}

func fmtText(e Event) {
	fmt.Println(e.Text)
}

func fmtPlan(e Event) {
	fmt.Fprintln(os.Stderr, "[loom] plan:")
	for i, step := range e.Steps {
		mark := " "
		switch step.Status {
		case "in_progress":
			mark = "~"
		case "done":
			mark = "x"
		}
		fmt.Fprintf(os.Stderr, "  %d. [%s] %s\n", i+1, mark, step.Title)
	}
}

func fmtToolResult(sessionID string, e Event) {
	fmt.Fprintf(os.Stderr, "[loom] action tool=%s engine=%s decision=%s risk=%.1f executed=%t outcome=%s session=%s\n",
		e.Tool, e.Engine, e.Decision, e.RiskScore, e.Executed, e.Outcome, sessionID)
}
