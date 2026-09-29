package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	"loom/internal/config"
	"loom/internal/governance"
)

type fakeEngine struct {
	response        *governance.EvaluateResponse
	actionCalls     int
	changeCalls     int
	lastChange      string // last change_type seen by EvaluateChange
	lastBlast       string // last blast_radius seen by EvaluateChange
	lastAffectsProd bool
}

func (f *fakeEngine) EvaluateAction(_ context.Context, _, _, _, _ string, affectsProduction bool) (*governance.EvaluateResponse, error) {
	f.actionCalls++
	f.lastAffectsProd = affectsProduction
	return f.response, nil
}

func (f *fakeEngine) EvaluateChange(_ context.Context, _ string, changeType string, _ string, _ string, affectsProduction bool, blastRadius string) (*governance.EvaluateResponse, error) {
	f.changeCalls++
	f.lastChange = changeType
	f.lastBlast = blastRadius
	f.lastAffectsProd = affectsProduction
	return f.response, nil
}

// TestDryRunSkipsMutatingToolsAndGovernance is the regression test for the
// gap found auditing Codex's changes: SetDryRun stored the flag on the
// Registry but Execute() never read it, so --dry-run silently ran every
// mutating command for real (including a rehearsed "terraform destroy").
// This locks in that a mutating tool call is short-circuited BEFORE
// governance is even consulted, per the roadmap's 2.2 acceptance criteria.
func TestDryRunSkipsMutatingToolsAndGovernance(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	registry.SetDryRun(true)

	out, err := registry.Execute(context.Background(), "powershell", map[string]any{
		"command": "terraform destroy -auto-approve",
	}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "dry-run") {
		t.Fatalf("output = %q, want a dry-run marker", out)
	}
	if engine.actionCalls != 0 || engine.changeCalls != 0 {
		t.Fatalf("actionCalls=%d changeCalls=%d, want 0/0: dry-run must not consult governance at all", engine.actionCalls, engine.changeCalls)
	}
}

// TestDryRunSkipsWriteFile locks in the same guarantee for write_file,
// which mutates the filesystem, not a shell command.
func TestDryRunSkipsWriteFile(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	registry.SetDryRun(true)

	dir := t.TempDir()
	path := dir + "/should-not-exist.txt"
	_, err := registry.Execute(context.Background(), "write_file", map[string]any{
		"path": path, "content": "hello",
	}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("write_file wrote to disk during dry-run: %v", statErr)
	}
	if engine.actionCalls != 0 {
		t.Fatalf("actionCalls = %d, want 0", engine.actionCalls)
	}
}

// TestDryRunStillExecutesReadOnlyTools ensures dry-run doesn't over-block:
// investigation must still work while rehearsing, and governance is still
// consulted for those calls exactly like a normal run.
func TestDryRunStillExecutesReadOnlyTools(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	registry.SetDryRun(true)

	dir := t.TempDir()
	path := dir + "/exists.txt"
	if err := os.WriteFile(path, []byte("hola"), 0644); err != nil {
		t.Fatalf("fixture setup: %v", err)
	}
	out, err := registry.Execute(context.Background(), "read_file", map[string]any{"path": path}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hola" {
		t.Fatalf("out = %q, want the real file contents even during dry-run", out)
	}
	if engine.actionCalls != 1 {
		t.Fatalf("actionCalls = %d, want 1: a read-only tool must still consult governance during dry-run", engine.actionCalls)
	}
}

func TestBlockedActionNeverExecutes(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block, Why: []string{"policy"}}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	_, err := registry.Execute(context.Background(), "powershell", map[string]any{"command": "echo SHOULD_NOT_RUN"}, false)
	if err == nil || !strings.Contains(err.Error(), "BLOQUEADO") {
		t.Fatalf("err = %v, want blocked error", err)
	}
	if engine.actionCalls != 1 {
		t.Fatalf("governance action calls = %d, want 1", engine.actionCalls)
	}
}

// TestDestructiveTerraformRoutesToEvaluateChange is the regression test for
// the real gap found in manual testing: "terraform destroy" run through the
// generic bash tool was scored by the generic policy engine (risk 8.0,
// ALLOW) instead of the infra engine that carries the blast-radius
// invariant. This asserts the routing, not DevMind's live decision.
func TestDestructiveTerraformRoutesToEvaluateChange(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Escalate, Why: []string{"blast radius org"}}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	registry.SetConfirmation(func(governance.Decision, *governance.EvaluateResponse) bool { return false })

	_, err := registry.Execute(context.Background(), "powershell", map[string]any{
		"command": "terraform destroy -target=aws_ebs_volume.production_db -auto-approve",
	}, true)

	if engine.changeCalls != 1 {
		t.Fatalf("EvaluateChange calls = %d, want 1 (destructive terraform must use the infra engine)", engine.changeCalls)
	}
	if engine.actionCalls != 0 {
		t.Fatalf("EvaluateAction calls = %d, want 0 (must not also hit the generic policy engine)", engine.actionCalls)
	}
	if engine.lastChange != "terraform_apply" {
		t.Fatalf("change_type = %q, want terraform_apply -- DevMind's real ChangeType enum has no separate destroy variant", engine.lastChange)
	}
	if engine.lastBlast != "org" {
		t.Fatalf("blast_radius = %q, want %q -- this is the exact gap found in manual testing: without an explicit blast_radius, DevMind's ORG-scope invariant never fires for a raw shell string", engine.lastBlast, "org")
	}
	if !engine.lastAffectsProd {
		t.Fatalf("affects_production = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "cancelada") {
		t.Fatalf("err = %v, want cancelled (ESCALATE without confirmation)", err)
	}
}

// TestDestructiveTerraformWithoutProductionHeuristicLeavesBlastRadiusEmpty
// ensures the "org" heuristic is scoped to the case we can actually justify:
// a destroy/delete/uninstall AND our production heuristic already fired.
// Anything else should defer to DevMind's own inference/regex scoring
// instead of Loom guessing a scope it has no basis for.
func TestDestructiveTerraformWithoutProductionHeuristicLeavesBlastRadiusEmpty(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})

	_, err := registry.Execute(context.Background(), "powershell", map[string]any{
		"command": "terraform destroy -target=aws_ebs_volume.staging_scratch -auto-approve",
	}, false) // affectsProduction = false
	_ = err // terraform binary isn't installed in CI -- only the governance call matters here

	if engine.lastBlast != "" {
		t.Fatalf("blast_radius = %q, want empty when the production heuristic did not fire", engine.lastBlast)
	}
}

// TestPlainBashStillUsesEvaluateAction ensures the fix is scoped: an
// ordinary shell command must keep going through the generic policy engine,
// not be misrouted to EvaluateChange.
func TestPlainBashStillUsesEvaluateAction(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})

	_, err := registry.Execute(context.Background(), "powershell", map[string]any{"command": "echo hello"}, false)
	_ = err // pwsh may not be installed in CI -- only the governance routing matters here
	if engine.actionCalls != 1 || engine.changeCalls != 0 {
		t.Fatalf("actionCalls=%d changeCalls=%d, want 1/0 for a plain command", engine.actionCalls, engine.changeCalls)
	}
}

// TestIacChangeTypesMatchDevMindEnum locks in the actual ChangeType enum
// values confirmed against DevMind's own core/types.py source
// (TERRAFORM_PLAN, TERRAFORM_APPLY, K8S_MANIFEST, HELM_RELEASE, ...). An
// earlier version of this code invented "terraform_destroy"/"kubectl_delete"/
// "helm_uninstall", none of which exist in DevMind's enum -- every call with
// those values was rejected with HTTP 400 by the live API. Destructive intent
// belongs in the payload text and blast_radius, not in change_type.
func TestIacChangeTypesMatchDevMindEnum(t *testing.T) {
	cases := map[string]string{
		"terraform destroy -target=x -auto-approve": "terraform_apply",
		"terraform apply -auto-approve":             "terraform_apply",
		"terraform plan":                            "terraform_plan",
		"kubectl delete pod foo":                    "k8s_manifest",
		"kubectl apply -f foo.yaml":                 "k8s_manifest",
		"helm uninstall myrelease":                  "helm_release",
		"helm install myrelease chart/":             "helm_release",
	}
	for command, want := range cases {
		got, isChange := iacChangeType(command)
		if !isChange {
			t.Fatalf("command %q: expected to classify as an infra change", command)
		}
		if got != want {
			t.Fatalf("command %q: change_type = %q, want %q", command, got, want)
		}
	}
}

// TestIacChangeTypesCoverKubernetesOperationalCommands locks in coverage for
// operational kubectl commands beyond apply/delete (scale, rollout, drain,
// cordon) -- these are common incident-response actions and must not fall
// through to the generic policy engine, which carries no blast-radius
// invariant.
func TestIacChangeTypesCoverKubernetesOperationalCommands(t *testing.T) {
	cases := []string{
		"kubectl scale deployment checkout --replicas=0",
		"kubectl rollout undo deployment/checkout",
		"kubectl rollout restart deployment/checkout",
		"kubectl drain node-42 --ignore-daemonsets",
		"kubectl cordon node-42",
	}
	for _, command := range cases {
		got, isChange := iacChangeType(command)
		if !isChange || got != "k8s_manifest" {
			t.Fatalf("command %q: change_type = %q isChange=%v, want k8s_manifest/true", command, got, isChange)
		}
	}
}

// TestIacChangeTypesCoverCloudIAMAndSecrets locks in coverage for IAM and
// secret-rotation commands across the three major cloud CLIs, mapped to the
// real DevMind enum values (not invented ones).
func TestIacChangeTypesCoverCloudIAMAndSecrets(t *testing.T) {
	cases := map[string]string{
		"aws iam attach-role-policy --role-name x --policy-arn y": "iam_change",
		"gcloud iam service-accounts create ci-deployer":          "iam_change",
		"az role assignment create --assignee x --role Owner":     "iam_change",
		"aws secretsmanager rotate-secret --secret-id db-prod":    "secret_rotation",
		"gcloud secrets versions add db-password --data-file=-":   "secret_rotation",
		"az keyvault secret set --name db-password --value x":     "secret_rotation",
	}
	for command, want := range cases {
		got, isChange := iacChangeType(command)
		if !isChange || got != want {
			t.Fatalf("command %q: change_type = %q isChange=%v, want %s/true", command, got, isChange, want)
		}
	}
}

// TestIacChangeTypesCoverDatabaseMigrations locks in schema_migration
// coverage for common migration tools.
func TestIacChangeTypesCoverDatabaseMigrations(t *testing.T) {
	cases := []string{
		"alembic upgrade head",
		"rails db:migrate",
		"flyway migrate",
		"prisma migrate deploy",
	}
	for _, command := range cases {
		got, isChange := iacChangeType(command)
		if !isChange || got != "schema_migration" {
			t.Fatalf("command %q: change_type = %q isChange=%v, want schema_migration/true", command, got, isChange)
		}
	}
}

// TestIacChangeTypesCoverDirectCloudResourceChanges locks in config_change
// coverage for destructive cloud CLI calls that bypass Terraform/Helm
// entirely -- ad hoc production changes are exactly what SRE governance
// needs to catch.
func TestIacChangeTypesCoverDirectCloudResourceChanges(t *testing.T) {
	cases := []string{
		"aws ec2 terminate-instances --instance-ids i-0123456789",
		"gcloud compute instances delete web-1 --zone=us-central1-a",
		"az vm delete --name web-1 --resource-group prod",
		"aws rds delete-db-instance --db-instance-identifier prod-db",
	}
	for _, command := range cases {
		got, isChange := iacChangeType(command)
		if !isChange || got != "config_change" {
			t.Fatalf("command %q: change_type = %q isChange=%v, want config_change/true", command, got, isChange)
		}
	}
}

// TestIacChangeTypeIgnoresReadOnlyCloudAndObservabilityCommands ensures
// read-only cloud/observability commands are NOT misclassified as infra
// changes -- these should fall through to the generic (low-risk) policy
// engine like any other read.
func TestIacChangeTypeIgnoresReadOnlyCloudAndObservabilityCommands(t *testing.T) {
	cases := []string{
		"aws ec2 describe-instances",
		"gcloud compute instances list",
		"kubectl get pods -n checkout",
		"kubectl describe deployment checkout",
		"curl -s http://prometheus:9090/api/v1/query?query=up",
	}
	for _, command := range cases {
		if _, isChange := iacChangeType(command); isChange {
			t.Fatalf("command %q: expected NOT to classify as an infra change", command)
		}
	}
}

func TestUpdatePlanToolIsRegistered(t *testing.T) {
	registry := NewRegistry(&fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}, "agent", "session", config.SreConfig{})
	for _, tool := range registry.List() {
		if tool.Name == "update_plan" {
			return
		}
	}
	t.Fatal("update_plan tool not registered, but the system prompt and event loop both depend on it")
}

// TestNativeSreToolsAreRegistered locks in that the native investigation
// tools actually get wired up, not just implemented and forgotten.
func TestNativeSreToolsAreRegistered(t *testing.T) {
	registry := NewRegistry(&fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}, "agent", "session", config.SreConfig{})
	want := []string{"k8s_get", "k8s_describe", "k8s_logs", "cloud_read", "metrics_query", "powershell", "read_file", "write_file", "update_plan"}
	got := map[string]bool{}
	for _, tool := range registry.List() {
		got[tool.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("tool %q not registered", name)
		}
	}
}

// TestCloudReadRejectsMutatingVerbsBeforeExecution is the defense-in-depth
// check for cloud_read: even though governance is consulted like any other
// tool call (that invariant is never bypassed -- see Registry.Execute), the
// tool itself must still refuse to actually run a mutating subcommand,
// since it is documented and schema'd to the model as read-only.
func TestCloudReadRejectsMutatingVerbsBeforeExecution(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})

	_, err := registry.Execute(context.Background(), "cloud_read", map[string]any{
		"provider": "aws",
		"args":     []any{"ec2", "terminate-instances", "--instance-ids", "i-0123456789"},
	}, false)

	if err == nil || !strings.Contains(err.Error(), "solo lectura") {
		t.Fatalf("err = %v, want a read-only rejection", err)
	}
	if engine.actionCalls != 1 {
		t.Fatalf("actionCalls = %d, want 1: governance is consulted for every tool call regardless", engine.actionCalls)
	}
}

func TestCloudReadAllowsDescribeVerb(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})

	// aws binary is very likely absent in CI -- only the pre-governance
	// verb check and the fact governance WAS consulted matter here.
	_, _ = registry.Execute(context.Background(), "cloud_read", map[string]any{
		"provider": "aws",
		"args":     []any{"ec2", "describe-instances"},
	}, false)

	if engine.actionCalls != 1 {
		t.Fatalf("actionCalls = %d, want 1: a read verb must reach governance", engine.actionCalls)
	}
}

// TestCloudMutatingCheckAllowsCommonReadCommands is the regression test for
// the substring matcher that rejected everyday reads: "--output" contains
// "put", "--format" contains "rm", "describe-addresses" contains "add",
// "list-attached-role-policies" contains "attach".
func TestCloudMutatingCheckAllowsCommonReadCommands(t *testing.T) {
	reads := [][]string{
		{"ec2", "describe-instances", "--output", "json"},
		{"compute", "instances", "list", "--format=json"},
		{"ec2", "describe-addresses"},
		{"iam", "list-attached-role-policies", "--role-name", "ci"},
		{"sts", "get-caller-identity"},
		{"logs", "filter-log-events", "--log-group-name", "/aws/eks/prod/cluster"},
		{"vm", "list", "--output", "table"},
	}
	for _, args := range reads {
		if isCloudMutatingCommand(args) {
			t.Fatalf("%v rejected as mutating, want allowed", args)
		}
	}
}

func TestCloudMutatingCheckStillRejectsWrites(t *testing.T) {
	writes := [][]string{
		{"ec2", "terminate-instances", "--instance-ids", "i-1"},
		{"secretsmanager", "put-secret-value", "--secret-id", "x"},
		{"rds", "delete-db-instance", "--db-instance-identifier", "prod"},
		{"ecr", "batch-delete-image", "--repository-name", "app"},
		{"ec2", "run-instances", "--image-id", "ami-1"},
		{"s3", "rm", "s3://bucket/key"},
		{"s3", "cp", "local.txt", "s3://bucket/key"},
		{"compute", "instances", "delete", "web-1"},
		{"projects", "set-iam-policy", "p", "policy.json"},
		{"EC2", "Terminate-Instances"},
	}
	for _, args := range writes {
		if !isCloudMutatingCommand(args) {
			t.Fatalf("%v allowed, want rejected as mutating", args)
		}
	}
}

func TestReviewRequiresConfirmation(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Review}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	registry.SetConfirmation(func(governance.Decision, *governance.EvaluateResponse) bool { return false })
	_, err := registry.Execute(context.Background(), "powershell", map[string]any{"command": "echo SHOULD_NOT_RUN"}, false)
	if err == nil || !strings.Contains(err.Error(), "cancelada") {
		t.Fatalf("err = %v, want cancelled error", err)
	}
}

func TestNonInteractiveReviewNeverExecutesTool(t *testing.T) {
	for _, decision := range []governance.Decision{governance.Review, governance.Escalate} {
		t.Run(string(decision), func(t *testing.T) {
			engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: decision}}
			registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
			registry.SetNonInteractive(true)

			executed := false
			confirmationRequested := false
			registry.SetConfirmation(func(governance.Decision, *governance.EvaluateResponse) bool {
				confirmationRequested = true
				return true
			})
			if err := registry.Register(Tool{
				Name: "requires-review",
				Run: func(context.Context, map[string]any) (string, error) {
					executed = true
					return "should not run", nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			_, err := registry.Execute(context.Background(), "requires-review", nil, false)
			if err == nil || !strings.Contains(err.Error(), "revision humana") || !strings.Contains(err.Error(), "no interactivo") {
				t.Fatalf("err = %v, want a non-interactive human-review error", err)
			}
			if confirmationRequested {
				t.Fatal("confirmation callback was called in non-interactive mode")
			}
			if executed {
				t.Fatalf("tool ran after a %s decision in non-interactive mode", decision)
			}
		})
	}
}

// TestToolSchemasDeclareRequiredFields is a regression test for the real gap
// found in manual testing: an empty {"type":"object"} schema with no
// properties gives the model zero grounding for argument key names, causing
// it to sometimes guess a key other than "command"/"path"/"content" -- which
// silently breaks both governance classification (iacChangeType never fires)
// and tool execution itself.
func TestToolSchemasDeclareRequiredFields(t *testing.T) {
	registry := NewRegistry(&fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Allow}}, "agent", "session", config.SreConfig{})
	want := map[string][]string{
		"powershell": {"command"},
		"read_file":  {"path"},
		"write_file": {"path", "content"},
	}
	for _, tool := range registry.List() {
		requiredFields, ok := want[tool.Name]
		if !ok {
			continue
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("tool %s: InputSchema has no properties -- model has no grounding for argument names", tool.Name)
		}
		required, ok := tool.InputSchema["required"].([]string)
		if !ok {
			t.Fatalf("tool %s: InputSchema has no required field list", tool.Name)
		}
		for _, field := range requiredFields {
			if _, present := props[field]; !present {
				t.Fatalf("tool %s: schema missing property %q", tool.Name, field)
			}
			found := false
			for _, r := range required {
				if r == field {
					found = true
				}
			}
			if !found {
				t.Fatalf("tool %s: %q not marked required", tool.Name, field)
			}
		}
	}
}
