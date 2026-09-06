package tools

import (
	"strings"
	"testing"
)

func TestSummarizeK8sJSONPodList(t *testing.T) {
	raw := []byte(`{
		"kind": "PodList",
		"items": [
			{
				"metadata": {"name": "checkout-7d9f-abc12", "namespace": "checkout", "creationTimestamp": "2020-01-01T00:00:00Z"},
				"spec": {"nodeName": "node-1"},
				"status": {
					"phase": "Running",
					"containerStatuses": [
						{"ready": true, "restartCount": 3},
						{"ready": false, "restartCount": 0}
					]
				}
			}
		]
	}`)
	summary, ok := summarizeK8sJSON("pods", raw)
	if !ok {
		t.Fatal("expected pods to be summarizable")
	}
	if !strings.Contains(summary, "checkout-7d9f-abc12") {
		t.Fatalf("summary missing pod name: %s", summary)
	}
	if !strings.Contains(summary, "1/2") {
		t.Fatalf("summary missing ready count 1/2: %s", summary)
	}
	if !strings.Contains(summary, "Running") {
		t.Fatalf("summary missing phase: %s", summary)
	}
	if !strings.Contains(summary, "3") {
		t.Fatalf("summary missing restart count: %s", summary)
	}
	if !strings.Contains(summary, "node-1") {
		t.Fatalf("summary missing node: %s", summary)
	}
	// AGE must be present as a compact unit, not the raw timestamp.
	if strings.Contains(summary, "2020-01-01") {
		t.Fatalf("summary leaked the raw timestamp instead of a compact age: %s", summary)
	}
}

func TestSummarizeK8sJSONSingleDeployment(t *testing.T) {
	// kubectl get deployment <name> -o json (no name given) returns a
	// single object, not a List -- this must work without an "items" key.
	raw := []byte(`{
		"metadata": {"name": "checkout", "creationTimestamp": "2020-01-01T00:00:00Z"},
		"status": {"replicas": 3, "readyReplicas": 2, "updatedReplicas": 3, "availableReplicas": 2}
	}`)
	summary, ok := summarizeK8sJSON("deployment", raw)
	if !ok {
		t.Fatal("expected deployment to be summarizable")
	}
	if !strings.Contains(summary, "checkout") || !strings.Contains(summary, "2/3") {
		t.Fatalf("summary = %s, want name and ready 2/3", summary)
	}
}

func TestSummarizeK8sJSONNodeRolesAndReadiness(t *testing.T) {
	raw := []byte(`{"items": [{
		"metadata": {
			"name": "node-1",
			"creationTimestamp": "2020-01-01T00:00:00Z",
			"labels": {"node-role.kubernetes.io/control-plane": ""}
		},
		"status": {
			"conditions": [{"type": "Ready", "status": "True"}],
			"nodeInfo": {"kubeletVersion": "v1.31.0"}
		}
	}]}`)
	summary, ok := summarizeK8sJSON("nodes", raw)
	if !ok {
		t.Fatal("expected nodes to be summarizable")
	}
	if !strings.Contains(summary, "Ready") || !strings.Contains(summary, "control-plane") || !strings.Contains(summary, "v1.31.0") {
		t.Fatalf("summary = %s, want Ready/control-plane/v1.31.0", summary)
	}
}

func TestSummarizeK8sJSONUnknownKindFallsBackToRaw(t *testing.T) {
	raw := []byte(`{"items": [{"metadata": {"name": "my-config"}}]}`)
	_, ok := summarizeK8sJSON("configmaps", raw)
	if ok {
		t.Fatal("configmaps is not a recognized kind and must fall back to raw JSON")
	}
}

func TestSummarizeK8sJSONInvalidJSONFallsBackToRaw(t *testing.T) {
	_, ok := summarizeK8sJSON("pods", []byte("not json"))
	if ok {
		t.Fatal("invalid JSON must fall back to raw output, not panic or fabricate a summary")
	}
}

func TestSummarizeK8sJSONEmptyListSaysSo(t *testing.T) {
	summary, ok := summarizeK8sJSON("pods", []byte(`{"items": []}`))
	if !ok || !strings.Contains(summary, "sin resultados") {
		t.Fatalf("summary=%q ok=%v, want a clear empty-result message", summary, ok)
	}
}

func TestSummarizePrometheusResponseCondensesVector(t *testing.T) {
	raw := []byte(`{
		"status": "success",
		"data": {"resultType": "vector", "result": [
			{"metric": {"__name__": "up", "instance": "checkout-1"}, "value": [1690000000, "1"]}
		]}
	}`)
	summary, ok := summarizePrometheusResponse(raw)
	if !ok {
		t.Fatal("expected a vector result to be summarizable")
	}
	if !strings.Contains(summary, "checkout-1") || !strings.Contains(summary, "1") {
		t.Fatalf("summary = %s, want instance label and value", summary)
	}
}

func TestSummarizePrometheusResponseDeclinesMatrix(t *testing.T) {
	raw := []byte(`{
		"status": "success",
		"data": {"resultType": "matrix", "result": [
			{"metric": {}, "values": [[1,"1"],[2,"2"]]}
		]}
	}`)
	_, ok := summarizePrometheusResponse(raw)
	if ok {
		t.Fatal("a range/matrix query must not be condensed -- it would discard the time series")
	}
}

func TestSummarizePrometheusResponseDeclinesErrorStatus(t *testing.T) {
	raw := []byte(`{"status": "error", "errorType": "bad_data", "error": "invalid query"}`)
	_, ok := summarizePrometheusResponse(raw)
	if ok {
		t.Fatal("an error response must fall back to raw formatting, not a fabricated summary")
	}
}

func TestSummarizeDatadogResponseCondensesLatestPoint(t *testing.T) {
	raw := []byte(`{
		"status": "ok",
		"series": [
			{"metric": "system.cpu.user", "scope": "host:checkout-1", "pointlist": [[1690000000000, 10.5], [1690000060000, 42.25]]}
		]
	}`)
	summary, ok := summarizeDatadogResponse(raw)
	if !ok {
		t.Fatal("expected a datadog ok response to be summarizable")
	}
	if !strings.Contains(summary, "system.cpu.user") || !strings.Contains(summary, "checkout-1") || !strings.Contains(summary, "42.25") {
		t.Fatalf("summary = %s, want metric/scope/latest point (42.25, not the earlier 10.5)", summary)
	}
}

func TestFormatObservabilityResponseHonorsRaw(t *testing.T) {
	raw := []byte(`{"status": "success", "data": {"resultType": "vector", "result": [{"metric": {}, "value": [1, "1"]}]}}`)
	out := formatObservabilityResponse(raw, true, summarizePrometheusResponse)
	if strings.Contains(out, "METRIC\tVALUE") || !strings.Contains(out, "\"resultType\"") {
		t.Fatalf("raw=true must return the full JSON, got: %s", out)
	}
}
