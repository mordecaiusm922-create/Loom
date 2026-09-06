package governance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
