package tools

import (
	"context"
	"testing"

	"github.com/mordecaiusm922-create/loom/internal/config"
	"github.com/mordecaiusm922-create/loom/internal/governance"
)

func sreWithEnvironments() config.SreConfig {
	return config.SreConfig{Environments: []config.EnvironmentConfig{
		{Name: "prod-us", Tier: "prod", KubeContexts: []string{"eks-main"}, CloudProfiles: []string{"acme-main"}, Paths: []string{"infra/live"}},
		{Name: "stg", Tier: "staging", KubeContexts: []string{"eks-stg"}},
	}}
}

func TestResolveEnvironmentDeclaredMappings(t *testing.T) {
	sre := sreWithEnvironments()
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"powershell", map[string]any{"command": "kubectl --context=eks-main delete pod x"}, config.TierProd},
		{"powershell", map[string]any{"command": "kubectl --context eks-stg delete pod x"}, config.TierStaging},
		{"powershell", map[string]any{"command": "aws rds delete-db-instance --profile acme-main"}, config.TierProd},
		{"powershell", map[string]any{"command": "terraform -chdir=infra/live/network apply"}, config.TierProd},
		{"cloud_read", map[string]any{"provider": "aws", "args": []any{"ec2", "describe-instances", "--profile", "acme-main"}}, config.TierProd},
	}
	for _, c := range cases {
		if got := ResolveEnvironment(c.tool, c.input, sre); got.Tier != c.want {
			t.Fatalf("%s %v: tier = %s (%s), want %s", c.tool, c.input, got.Tier, got.Reason, c.want)
		}
	}
}

// TestResolveEnvironmentUsesInjectedKubeContext is the regression test for
// the false negative in the old looksLikeProduction: native k8s_* tools
// inject sre.kube_context themselves, so it never appeared in the model's
// input and a prod cluster was never flagged as production.
func TestResolveEnvironmentUsesInjectedKubeContext(t *testing.T) {
	sre := config.SreConfig{KubeContext: "prod-eks"}
	got := ResolveEnvironment("k8s_get", map[string]any{"resource": "pods"}, sre)
	if got.Tier != config.TierProd {
		t.Fatalf("tier = %s (%s), want prod from the injected kube_context", got.Tier, got.Reason)
	}

	sre = sreWithEnvironments()
	sre.KubeContext = "eks-main" // no "prod" in the name at all
	if got := ResolveEnvironment("k8s_logs", map[string]any{"pod": "checkout-1"}, sre); got.Tier != config.TierProd {
		t.Fatalf("tier = %s (%s), want prod from declared mapping of the injected context", got.Tier, got.Reason)
	}
}

func TestResolveEnvironmentKeywordsAreWholeWords(t *testing.T) {
	sre := config.SreConfig{}
	notProd := []string{
		"kubectl get pods -n product-catalog",
		"cat reproducible-build.log",
		"echo producer",
	}
	for _, command := range notProd {
		if got := ResolveEnvironment("powershell", map[string]any{"command": command}, sre); got.Tier == config.TierProd {
			t.Fatalf("%q resolved to prod (%s); substring matches must not count", command, got.Reason)
		}
	}
	prod := []string{
		"terraform destroy -target=aws_ebs_volume.production_db",
		"kubectl --context prod-eks scale deploy/x --replicas=0",
		"helm upgrade api ./chart -f values/prd.yaml",
	}
	for _, command := range prod {
		if got := ResolveEnvironment("powershell", map[string]any{"command": command}, sre); got.Tier != config.TierProd {
			t.Fatalf("%q resolved to %s, want prod", command, got.Tier)
		}
	}
}

// A production word always wins over a non-production word in the same
// call: "copy from staging to prod" is a production change.
func TestResolveEnvironmentProdWinsOverNonProd(t *testing.T) {
	got := ResolveEnvironment("powershell", map[string]any{"command": "pg_dump staging | psql prod"}, config.SreConfig{})
	if got.Tier != config.TierProd {
		t.Fatalf("tier = %s, want prod", got.Tier)
	}
}

func TestResolveEnvironmentTypoTierIsUnknownNotDowngraded(t *testing.T) {
	sre := config.SreConfig{Environments: []config.EnvironmentConfig{{Name: "x", Tier: "prodution", KubeContexts: []string{"eks-main"}}}}
	got := ResolveEnvironment("powershell", map[string]any{"command": "kubectl --context eks-main apply -f x.yaml"}, sre)
	if got.Tier != config.TierUnknown {
		t.Fatalf("tier = %s, want unknown for an unrecognized declared tier", got.Tier)
	}
}

// TestUnknownEnvironmentInfraChangeIsTreatedAsProduction locks in the
// fail-safe: a mutation whose target cannot be resolved reaches DevMind as
// affects_production=true, with blast_radius=org when it is destructive.
func TestUnknownEnvironmentInfraChangeIsTreatedAsProduction(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})

	_, _ = registry.Execute(context.Background(), "powershell", map[string]any{"command": "kubectl delete namespace checkout"}, false)
	if engine.changeCalls != 1 {
		t.Fatalf("changeCalls = %d, want 1", engine.changeCalls)
	}
	if !engine.lastAffectsProd || engine.lastBlast != "org" {
		t.Fatalf("affects_production=%v blast=%q, want true/org for an unresolved destructive change", engine.lastAffectsProd, engine.lastBlast)
	}
}

// Reads against an unknown target stay non-production so an unmapped
// cluster does not turn every investigation step into a REVIEW.
func TestUnknownEnvironmentReadIsNotProduction(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{})
	_, _ = registry.Execute(context.Background(), "k8s_get", map[string]any{"resource": "pods"}, false)
	if engine.lastAffectsProd {
		t.Fatal("affects_production = true for a read against an unknown environment")
	}
}

func TestNativeReadAgainstProdContextReachesGovernanceAsProduction(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block}}
	registry := NewRegistry(engine, "agent", "session", config.SreConfig{KubeContext: "prod-eks"})
	_, _ = registry.Execute(context.Background(), "k8s_get", map[string]any{"resource": "pods"}, false)
	if !engine.lastAffectsProd {
		t.Fatal("affects_production = false for k8s_get against sre.kube_context=prod-eks")
	}
}

func TestDeclaredStagingDestroyIsNotProduction(t *testing.T) {
	engine := &fakeEngine{response: &governance.EvaluateResponse{DecisionValue: governance.Block}}
	registry := NewRegistry(engine, "agent", "session", sreWithEnvironments())
	_, _ = registry.Execute(context.Background(), "powershell", map[string]any{"command": "kubectl --context eks-stg delete ns scratch"}, false)
	if engine.lastAffectsProd || engine.lastBlast != "" {
		t.Fatalf("affects_production=%v blast=%q, want false/empty for a declared staging target", engine.lastAffectsProd, engine.lastBlast)
	}
}
