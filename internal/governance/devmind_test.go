package governance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDevMindHTTPFailureFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	engine := NewDevMindEngine(server.URL, "")
	decision, err := engine.EvaluateAction(context.Background(), "agent", "bash", "execute", "rm", true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionValue != Review {
		t.Fatalf("decision = %q, want REVIEW", decision.DecisionValue)
	}
}

// TestEvaluateChangeHitsEvaluateChangeEndpoint confirms EvaluateChange
// posts to /evaluate-change (the infra engine), not /evaluate -- the two
// engines are not interchangeable, and this is the endpoint that carries
// the blast-radius invariant.
func TestEvaluateChangeHitsEvaluateChangeEndpoint(t *testing.T) {
	var gotPath string
	var gotBody evaluateChangeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"ESCALATE","risk_score":15.0,"escalation_required":true}`))
	}))
	defer server.Close()

	engine := NewDevMindEngine(server.URL, "")
	decision, err := engine.EvaluateChange(context.Background(), "agent", "terraform_destroy", "infrastructure",
		"terraform destroy -target=aws_ebs_volume.production_db -auto-approve", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/evaluate-change" {
		t.Fatalf("path = %q, want /evaluate-change", gotPath)
	}
	if gotBody.ChangeType != "terraform_destroy" || gotBody.Surface != "infrastructure" {
		t.Fatalf("unexpected request body: %+v", gotBody)
	}
	if decision.DecisionValue != Escalate || !decision.EscalationRequired {
		t.Fatalf("decision = %+v, want ESCALATE with escalation_required", decision)
	}
}

func TestDecisionCarriesDevMindAuditID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"REVIEW","risk_score":6.5,"why":["prod"],"audit_id":"act-7f3a9c"}`))
	}))
	defer server.Close()
	decision, err := NewDevMindEngine(server.URL, "").EvaluateAction(context.Background(), "agent", "k8s_get", "execute", "pods", true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.AuditID != "act-7f3a9c" {
		t.Fatalf("AuditID = %q, want act-7f3a9c", decision.AuditID)
	}
}

func TestEvaluateChangeFailsClosedOnTransportError(t *testing.T) {
	engine := NewDevMindEngine("http://127.0.0.1:0", "") // unreachable
	decision, err := engine.EvaluateChange(context.Background(), "agent", "terraform_destroy", "infrastructure", "x", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionValue != Review {
		t.Fatalf("decision = %q, want REVIEW (fail-closed)", decision.DecisionValue)
	}
}

func TestDevMindRejectsUnknownDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"MAYBE"}`))
	}))
	defer server.Close()
	engine := NewDevMindEngine(server.URL, "")
	decision, err := engine.EvaluateAction(context.Background(), "agent", "bash", "execute", "echo hi", false)
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionValue != Review {
		t.Fatalf("decision = %q, want REVIEW", decision.DecisionValue)
	}
}

func TestCheckRequiresToken(t *testing.T) {
	engine := NewDevMindEngine("http://127.0.0.1:1", "")
	if err := engine.Check(context.Background(), "loom-sre"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestCheckSendsTokenAndAcceptsValidDecision(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody evaluateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"decision":"ALLOW","risk_score":2}`))
	}))
	defer server.Close()

	engine := NewDevMindEngine(server.URL, "dvm_test")
	if err := engine.Check(context.Background(), "loom-sre"); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
	if gotAuth != "Bearer dvm_test" || gotPath != "/evaluate" {
		t.Fatalf("auth=%q path=%q", gotAuth, gotPath)
	}
	if gotBody.AgentID != "loom-sre" || gotBody.Operation != "read" {
		t.Fatalf("probe must be a read by the configured agent, got %+v", gotBody)
	}
}

func TestCheckExplainsEachFailure(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, `{}`, "HTTP 401"},
		{http.StatusForbidden, `{}`, "LOOM_AGENT_ID"},
		{http.StatusServiceUnavailable, `{}`, "HTTP 503"},
		{http.StatusInternalServerError, `{}`, "HTTP 500"},
		{http.StatusOK, `{"decision":"MAYBE"}`, "unexpected response"},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		err := NewDevMindEngine(server.URL, "dvm_test").Check(context.Background(), "loom-sre")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("status %d: err = %v, want it to mention %q", tc.status, err, tc.want)
		}
	}
}

func TestCheckReportsUnreachableService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	if err := NewDevMindEngine(url, "dvm_test").Check(context.Background(), "loom-sre"); err == nil || !strings.Contains(err.Error(), "could not reach DevMind") {
		t.Fatalf("err = %v, want unreachable error", err)
	}
}

func TestRejectedTokenFailsClosedWithHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	decision, err := NewDevMindEngine(server.URL, "").EvaluateAction(context.Background(), "agent", "bash", "execute", "ls", false)
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionValue != Review {
		t.Fatalf("decision = %q, want REVIEW", decision.DecisionValue)
	}
	if len(decision.Why) == 0 || !strings.Contains(decision.Why[0], "DEVMIND_TOKEN") {
		t.Fatalf("why = %v, want a DEVMIND_TOKEN hint", decision.Why)
	}
}
