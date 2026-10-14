// Package governance keeps the policy decision outside the model and outside
// the tool implementation. DevMind is therefore a control plane, not advice.
package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Decision string

const (
	Allow    Decision = "ALLOW"
	Review   Decision = "REVIEW"
	Block    Decision = "BLOCK"
	Escalate Decision = "ESCALATE"
	Rewrite  Decision = "REWRITE"
)

type EvaluateResponse struct {
	DecisionValue      Decision `json:"decision"`
	RiskScore          float64  `json:"risk_score"`
	Why                []string `json:"why"`
	EscalationRequired bool     `json:"escalation_required"`
	// AuditID is DevMind's identifier for the decision record it stored.
	// Loom persists it next to the local outcome so a postmortem can join
	// the local timeline with DevMind's audit trail. Empty on a local
	// fail-closed decision (DevMind never saw the request).
	AuditID string `json:"audit_id,omitempty"`
}

// Engine is the governance contract. EvaluateAction covers generic tool
// calls (terminal/filesystem/db/cloud) via DevMind's policy engine.
// EvaluateChange covers infrastructure changes (Terraform/K8s/Helm) via
// DevMind's infra engine -- the only path that carries the blast-radius
// invariant (ORG/ACCOUNT scope always escalates) and Terraform/K8s
// semantic parsing. A destructive terraform/kubectl/helm command run
// through "bash" MUST be routed to EvaluateChange, not EvaluateAction,
// or the blast-radius invariant is silently unreachable.
type Engine interface {
	EvaluateAction(ctx context.Context, agentID, tool, operation, payload string, affectsProduction bool) (*EvaluateResponse, error)
	EvaluateChange(ctx context.Context, agentID, changeType, surface, payload string, affectsProduction bool, blastRadius string) (*EvaluateResponse, error)
}

// NoopEngine is only used after an explicit local unsafe opt-out.
type NoopEngine struct{}

func (NoopEngine) EvaluateAction(context.Context, string, string, string, string, bool) (*EvaluateResponse, error) {
	return &EvaluateResponse{DecisionValue: Allow}, nil
}

func (NoopEngine) EvaluateChange(context.Context, string, string, string, string, bool, string) (*EvaluateResponse, error) {
	return &EvaluateResponse{DecisionValue: Allow}, nil
}

// Name identifies the engine in operator-facing messages.
func (*DevMindEngine) Name() string { return "DevMind" }

// Name identifies the engine in operator-facing messages.
func (NoopEngine) Name() string { return "no governance (unsafe opt-out)" }

type DevMindEngine struct {
	BaseURL string
	Token   string
	client  *http.Client
}

// DefaultDevMindURL is DevMind's REST API, served under /v1 by the same
// always-on service as its MCP endpoint.
const DefaultDevMindURL = "https://devmind-mcp.onrender.com/v1"

func NewDevMindEngine(baseURL, token string) *DevMindEngine {
	if baseURL == "" {
		baseURL = DefaultDevMindURL
	}
	return &DevMindEngine{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, client: &http.Client{Timeout: 30 * time.Second}}
}

type evaluateRequest struct {
	AgentID           string `json:"agent_id"`
	Tool              string `json:"tool"`
	Operation         string `json:"operation"`
	Payload           string `json:"payload"`
	AffectsProduction bool   `json:"affects_production,omitempty"`
}

type evaluateChangeRequest struct {
	AgentID           string `json:"agent_id"`
	ChangeType        string `json:"change_type"`
	Surface           string `json:"surface"`
	Payload           string `json:"payload"`
	AffectsProduction bool   `json:"affects_production,omitempty"`
	BlastRadius       string `json:"blast_radius,omitempty"`
}

// EvaluateAction fails closed: transport failures, HTTP failures, malformed
// bodies, and unknown decisions are all converted to REVIEW, never ALLOW.
func (d *DevMindEngine) EvaluateAction(ctx context.Context, agentID, tool, operation, payload string, affectsProduction bool) (*EvaluateResponse, error) {
	body, err := json.Marshal(evaluateRequest{AgentID: agentID, Tool: tool, Operation: operation, Payload: payload, AffectsProduction: affectsProduction})
	if err != nil {
		return nil, err
	}
	return d.post(ctx, "/evaluate", body)
}

// EvaluateChange calls DevMind's infra engine (Terraform/K8s/Helm), the
// only path carrying the blast-radius invariant and semantic parsing.
// Same fail-closed guarantees as EvaluateAction.
func (d *DevMindEngine) EvaluateChange(ctx context.Context, agentID, changeType, surface, payload string, affectsProduction bool, blastRadius string) (*EvaluateResponse, error) {
	body, err := json.Marshal(evaluateChangeRequest{
		AgentID:           agentID,
		ChangeType:        changeType,
		Surface:           surface,
		Payload:           payload,
		AffectsProduction: affectsProduction,
		BlastRadius:       blastRadius,
	})
	if err != nil {
		return nil, err
	}
	return d.post(ctx, "/evaluate-change", body)
}

// retryBackoff spaces out retries of transient failures. Hosted DevMind can
// take tens of seconds to wake from idle; without retries the first action
// after a quiet period would fail closed to REVIEW for no real reason.
var retryBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// post retries transient failures (transport errors and HTTP 502/503/504)
// and then fails closed: every failure that survives the retries becomes
// REVIEW, never ALLOW.
func (d *DevMindEngine) post(ctx context.Context, path string, body []byte) (*EvaluateResponse, error) {
	for attempt := 0; ; attempt++ {
		decision, reason, retryable := d.postOnce(ctx, path, body)
		if decision != nil {
			return decision, nil
		}
		if !retryable || attempt >= len(retryBackoff) {
			if attempt > 0 {
				reason = fmt.Sprintf("%s (after %d attempts)", reason, attempt+1)
			}
			return failClosed(reason), nil
		}
		select {
		case <-ctx.Done():
			return failClosed(reason), nil
		case <-time.After(retryBackoff[attempt]):
		}
	}
}

func (d *DevMindEngine) postOnce(ctx context.Context, path string, body []byte) (decision *EvaluateResponse, reason string, retryable bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Sprintf("could not build the DevMind request: %v", err), false
	}
	req.Header.Set("Content-Type", "application/json")
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("could not reach DevMind: %v", err), ctx.Err() == nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, fmt.Sprintf("could not read the DevMind response: %v", readErr), true
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := fmt.Sprintf("DevMind returned HTTP %d", resp.StatusCode)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			reason += " (token missing or rejected: set DEVMIND_TOKEN and run loom doctor)"
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return nil, reason, true
		}
		return nil, reason, false
	}
	var out EvaluateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "invalid response from DevMind", false
	}
	if !validDecision(out.DecisionValue) {
		return nil, fmt.Sprintf("invalid decision from DevMind: %q", out.DecisionValue), false
	}
	return &out, "", false
}

// ErrNoToken means no DevMind token is configured at all.
var ErrNoToken = errors.New("no DevMind token configured: export DEVMIND_TOKEN=dvm_...")

// Check verifies, for `loom doctor`, that DevMind is reachable and accepts
// the configured token, by evaluating one harmless read-only probe action.
// Unlike EvaluateAction it never turns a failure into a decision: it
// returns an error that says exactly what to fix. The deadline comes from
// ctx, so the caller can allow for a cold start of the hosted service.
func (d *DevMindEngine) Check(ctx context.Context, agentID string) error {
	if strings.TrimSpace(d.Token) == "" {
		return ErrNoToken
	}
	body, err := json.Marshal(evaluateRequest{
		AgentID:   agentID,
		Tool:      "loom_doctor",
		Operation: "read",
		Payload:   "loom doctor: connectivity and token check",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/evaluate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach DevMind at %s: %w", d.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		var decision EvaluateResponse
		if err := json.Unmarshal(raw, &decision); err != nil || !validDecision(decision.DecisionValue) {
			return fmt.Errorf("DevMind answered with an unexpected response body")
		}
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("DevMind rejected the token (HTTP 401): it is invalid, revoked, or issued for another DevMind service")
	case http.StatusForbidden:
		return fmt.Errorf("the token is bound to a different agent (HTTP 403): set LOOM_AGENT_ID to the agent it was issued for (now %q)", agentID)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("DevMind is temporarily unavailable (HTTP 503): it may be waking up; run loom doctor again in a minute")
	default:
		return fmt.Errorf("DevMind returned HTTP %d", resp.StatusCode)
	}
}

func validDecision(decision Decision) bool {
	switch decision {
	case Allow, Review, Block, Escalate, Rewrite:
		return true
	default:
		return false
	}
}

func failClosed(reason string) *EvaluateResponse {
	return &EvaluateResponse{DecisionValue: Review, Why: []string{reason + "; failed closed to REVIEW"}}
}
