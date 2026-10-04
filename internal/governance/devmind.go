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

type DevMindEngine struct {
	BaseURL string
	Token   string
	client  *http.Client
}

func NewDevMindEngine(baseURL, token string) *DevMindEngine {
	if baseURL == "" {
		baseURL = "https://devmind-2cej.onrender.com"
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

func (d *DevMindEngine) post(ctx context.Context, path string, body []byte) (*EvaluateResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
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
		return failClosed(fmt.Sprintf("no se pudo contactar DevMind: %v", err)), nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return failClosed(fmt.Sprintf("no se pudo leer respuesta de DevMind: %v", readErr)), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := fmt.Sprintf("DevMind devolvio HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			reason += " (token ausente o rechazado: define DEVMIND_TOKEN y corre loom doctor)"
		}
		return failClosed(reason), nil
	}
	var decision EvaluateResponse
	if err := json.Unmarshal(raw, &decision); err != nil {
		return failClosed("respuesta invalida de DevMind"), nil
	}
	if !validDecision(decision.DecisionValue) {
		return failClosed(fmt.Sprintf("decision invalida de DevMind: %q", decision.DecisionValue)), nil
	}
	return &decision, nil
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
		return fmt.Errorf("DevMind could not validate the token (HTTP 503): the token may not exist, or the service is unavailable")
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
	return &EvaluateResponse{DecisionValue: Review, Why: []string{reason + "; fail-closed a REVIEW"}}
}
