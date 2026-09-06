package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// -- Kubernetes: condense `kubectl get -o json` -------------------------
//
// kubectl's own default table output is already reasonably compact, so this
// only kicks in when the caller explicitly asks for `output: "json"` (the
// case that would otherwise dump a full manifest, including things like
// managedFields, per pod/node/deployment). It intentionally covers the
// handful of kinds an SRE actually looks at over and over during an
// incident, not every possible resource/CRD -- an unrecognized kind falls
// back to the JSON kubectl already returned, unchanged.

type k8sSummarizer func(items []map[string]any) (header []string, rows [][]string)

var k8sKindAliases = map[string]string{
	"po": "pod", "pods": "pod", "pod": "pod",
	"deploy": "deployment", "deployments": "deployment", "deployment": "deployment",
	"no": "node", "nodes": "node", "node": "node",
	"svc": "service", "services": "service", "service": "service",
	"ev": "event", "events": "event", "event": "event",
	"rs": "replicaset", "replicasets": "replicaset", "replicaset": "replicaset",
	"sts": "statefulset", "statefulsets": "statefulset", "statefulset": "statefulset",
	"ds": "daemonset", "daemonsets": "daemonset", "daemonset": "daemonset",
}

var k8sSummarizers = map[string]k8sSummarizer{
	"pod":         summarizePods,
	"deployment":  summarizeReplicaWorkload,
	"replicaset":  summarizeReplicaWorkload,
	"statefulset": summarizeReplicaWorkload,
	"daemonset":   summarizeReplicaWorkload,
	"node":        summarizeNodes,
	"service":     summarizeServices,
	"event":       summarizeEvents,
}

func normalizeK8sKind(kind string) string {
	lower := strings.ToLower(strings.TrimSpace(kind))
	if alias, ok := k8sKindAliases[lower]; ok {
		return alias
	}
	return lower
}

// summarizeK8sJSON returns (summary, true) when it recognizes the resource
// kind, or ("", false) when the caller should fall back to the raw JSON
// kubectl returned -- including when raw isn't even valid JSON.
func summarizeK8sJSON(resourceKind string, raw []byte) (string, bool) {
	summarizer, ok := k8sSummarizers[normalizeK8sKind(resourceKind)]
	if !ok {
		return "", false
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", false
	}
	var items []map[string]any
	if rawItems, isList := generic["items"].([]any); isList {
		for _, it := range rawItems {
			if m, ok := it.(map[string]any); ok {
				items = append(items, m)
			}
		}
	} else {
		items = []map[string]any{generic}
	}
	if len(items) == 0 {
		return "(sin resultados)", true
	}
	header, rows := summarizer(items)
	return renderTable(header, rows), true
}

func renderTable(header []string, rows [][]string) string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

func getPath(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = cm[p]
	}
	return cur
}

func getStringPath(m map[string]any, path ...string) string {
	s, _ := getPath(m, path...).(string)
	return s
}

func getFloatPath(m map[string]any, path ...string) (float64, bool) {
	f, ok := getPath(m, path...).(float64)
	return f, ok
}

// ageString mimics kubectl's compact AGE column: the largest whole unit
// that fits, not a full "3h12m5s" duration.
func ageString(since time.Duration) string {
	switch {
	case since < time.Minute:
		return fmt.Sprintf("%ds", int(since.Seconds()))
	case since < time.Hour:
		return fmt.Sprintf("%dm", int(since.Minutes()))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh", int(since.Hours()))
	default:
		return fmt.Sprintf("%dd", int(since.Hours()/24))
	}
}

func ageFromTimestamp(m map[string]any, path ...string) string {
	ts := getPath(m, path...)
	s, ok := ts.(string)
	if !ok || s == "" {
		return "<unknown>"
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "<unknown>"
	}
	return ageString(time.Since(parsed))
}

func summarizePods(items []map[string]any) ([]string, [][]string) {
	header := []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE", "NODE"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		name := getStringPath(item, "metadata", "name")
		phase := getStringPath(item, "status", "phase")
		node := getStringPath(item, "spec", "nodeName")
		age := ageFromTimestamp(item, "metadata", "creationTimestamp")

		containerStatuses, _ := getPath(item, "status", "containerStatuses").([]any)
		ready, total, restarts := 0, 0, 0
		for _, raw := range containerStatuses {
			cs, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			total++
			if r, ok := cs["ready"].(bool); ok && r {
				ready++
			}
			if rc, ok := cs["restartCount"].(float64); ok {
				restarts += int(rc)
			}
		}
		rows = append(rows, []string{name, fmt.Sprintf("%d/%d", ready, total), phase, fmt.Sprintf("%d", restarts), age, node})
	}
	return header, rows
}

// summarizeReplicaWorkload covers Deployments, ReplicaSets, StatefulSets,
// and DaemonSets -- they all expose the same replica/ready/updated/
// available shape under .status.
func summarizeReplicaWorkload(items []map[string]any) ([]string, [][]string) {
	header := []string{"NAME", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		name := getStringPath(item, "metadata", "name")
		age := ageFromTimestamp(item, "metadata", "creationTimestamp")
		replicas, _ := getFloatPath(item, "status", "replicas")
		ready, _ := getFloatPath(item, "status", "readyReplicas")
		updated, _ := getFloatPath(item, "status", "updatedReplicas")
		available, _ := getFloatPath(item, "status", "availableReplicas")
		rows = append(rows, []string{
			name,
			fmt.Sprintf("%d/%d", int(ready), int(replicas)),
			fmt.Sprintf("%d", int(updated)),
			fmt.Sprintf("%d", int(available)),
			age,
		})
	}
	return header, rows
}

func summarizeNodes(items []map[string]any) ([]string, [][]string) {
	header := []string{"NAME", "STATUS", "ROLES", "AGE", "VERSION"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		name := getStringPath(item, "metadata", "name")
		age := ageFromTimestamp(item, "metadata", "creationTimestamp")
		version := getStringPath(item, "status", "nodeInfo", "kubeletVersion")

		status := "Unknown"
		if conditions, ok := getPath(item, "status", "conditions").([]any); ok {
			for _, raw := range conditions {
				cond, ok := raw.(map[string]any)
				if !ok || cond["type"] != "Ready" {
					continue
				}
				if cond["status"] == "True" {
					status = "Ready"
				} else {
					status = "NotReady"
				}
			}
		}

		var roles []string
		if labels, ok := getPath(item, "metadata", "labels").(map[string]any); ok {
			for label := range labels {
				const prefix = "node-role.kubernetes.io/"
				if strings.HasPrefix(label, prefix) {
					roles = append(roles, strings.TrimPrefix(label, prefix))
				}
			}
		}
		sort.Strings(roles)
		roleStr := strings.Join(roles, ",")
		if roleStr == "" {
			roleStr = "<none>"
		}
		rows = append(rows, []string{name, status, roleStr, age, version})
	}
	return header, rows
}

func summarizeServices(items []map[string]any) ([]string, [][]string) {
	header := []string{"NAME", "TYPE", "CLUSTER-IP", "PORTS", "AGE"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		name := getStringPath(item, "metadata", "name")
		age := ageFromTimestamp(item, "metadata", "creationTimestamp")
		svcType := getStringPath(item, "spec", "type")
		clusterIP := getStringPath(item, "spec", "clusterIP")

		var ports []string
		if rawPorts, ok := getPath(item, "spec", "ports").([]any); ok {
			for _, rp := range rawPorts {
				p, ok := rp.(map[string]any)
				if !ok {
					continue
				}
				port, _ := p["port"].(float64)
				proto, _ := p["protocol"].(string)
				if proto == "" {
					proto = "TCP"
				}
				ports = append(ports, fmt.Sprintf("%d/%s", int(port), proto))
			}
		}
		portStr := strings.Join(ports, ",")
		if portStr == "" {
			portStr = "<none>"
		}
		rows = append(rows, []string{name, svcType, clusterIP, portStr, age})
	}
	return header, rows
}

func summarizeEvents(items []map[string]any) ([]string, [][]string) {
	header := []string{"TYPE", "REASON", "OBJECT", "MESSAGE", "AGE", "COUNT"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		eventType := getStringPath(item, "type")
		reason := getStringPath(item, "reason")
		message := getStringPath(item, "message")
		objectKind := getStringPath(item, "involvedObject", "kind")
		objectName := getStringPath(item, "involvedObject", "name")
		object := strings.TrimSuffix(fmt.Sprintf("%s/%s", objectKind, objectName), "/")

		age := ageFromTimestamp(item, "lastTimestamp")
		if age == "<unknown>" {
			age = ageFromTimestamp(item, "eventTime")
		}
		count := 1
		if c, ok := getFloatPath(item, "count"); ok && c > 0 {
			count = int(c)
		}
		rows = append(rows, []string{eventType, reason, object, message, age, fmt.Sprintf("%d", count)})
	}
	return header, rows
}

// -- Observability: condense Prometheus/Grafana/Datadog query responses --
//
// A raw query response carries a lot of protocol envelope (status,
// resultType, per-series metadata) around what an SRE actually wants during
// an incident: which series, and what value. Anything this doesn't
// recognize (range/matrix results, error responses) falls back to the full
// pretty-printed JSON -- summarizing "the last of many points" for a range
// query would silently discard the trend that made it worth graphing.

// summarizePrometheusResponse handles both Prometheus's native API and
// Grafana's Prometheus-datasource proxy, which return the same envelope.
func summarizePrometheusResponse(raw []byte) (string, bool) {
	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false
	}
	if parsed.Status != "success" {
		return "", false
	}
	if parsed.Data.ResultType != "vector" && parsed.Data.ResultType != "scalar" {
		// range/matrix results carry a time series per entry -- reducing
		// that to one line would throw away the trend, so show it raw.
		return "", false
	}
	if len(parsed.Data.Result) == 0 {
		return "(sin resultados)", true
	}
	header := []string{"METRIC", "VALUE"}
	rows := make([][]string, 0, len(parsed.Data.Result))
	for _, series := range parsed.Data.Result {
		value := "?"
		if len(series.Value) == 2 {
			value = fmt.Sprintf("%v", series.Value[1])
		}
		rows = append(rows, []string{formatLabels(series.Metric), value})
	}
	return renderTable(header, rows), true
}

// summarizeDatadogResponse handles Datadog's v1 metrics query response.
func summarizeDatadogResponse(raw []byte) (string, bool) {
	var parsed struct {
		Status string `json:"status"`
		Series []struct {
			Metric    string      `json:"metric"`
			Scope     string      `json:"scope"`
			Pointlist [][]float64 `json:"pointlist"`
		} `json:"series"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false
	}
	if parsed.Status != "ok" {
		return "", false
	}
	if len(parsed.Series) == 0 {
		return "(sin resultados)", true
	}
	header := []string{"METRIC", "SCOPE", "LATEST"}
	rows := make([][]string, 0, len(parsed.Series))
	for _, series := range parsed.Series {
		latest := "sin datos"
		if n := len(series.Pointlist); n > 0 && len(series.Pointlist[n-1]) == 2 {
			latest = fmt.Sprintf("%.4g", series.Pointlist[n-1][1])
		}
		rows = append(rows, []string{series.Metric, series.Scope, latest})
	}
	return renderTable(header, rows), true
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// formatObservabilityResponse applies summarizer unless raw is requested or
// the summarizer declines (returns ok=false, e.g. an error response or a
// shape it doesn't recognize) -- in which case it falls back to indented
// JSON, same behavior as before this file existed.
func formatObservabilityResponse(body []byte, raw bool, summarizer func([]byte) (string, bool)) string {
	if !raw && summarizer != nil {
		if summary, ok := summarizer(body); ok {
			return summary
		}
	}
	var pretty map[string]any
	if json.Unmarshal(body, &pretty) == nil {
		if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			return string(formatted)
		}
	}
	return string(body)
}
