package tools

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mordecaiusm922-create/loom/internal/config"
)

// Environment is the resolved target of one tool call.
type Environment struct {
	Name   string
	Tier   string
	Reason string
}

var prodWords = map[string]bool{"prod": true, "production": true, "produccion": true, "prd": true, "live": true}

var nonProdWords = map[string]string{
	"staging": config.TierStaging, "stage": config.TierStaging, "stg": config.TierStaging,
	"preprod": config.TierStaging, "qa": config.TierStaging, "uat": config.TierStaging,
	"dev": config.TierDev, "development": config.TierDev, "sandbox": config.TierDev,
	"test": config.TierDev, "local": config.TierDev,
}

// ResolveEnvironment decides which environment a tool call targets.
//
// Order: (1) an identifier declared in sre.environments, matched exactly
// against the call's tokens and the context/profile the native tools
// inject; (2) a whole-word keyword heuristic, where a production word always
// wins over a non-production one; (3) TierUnknown.
//
// This replaces a substring check for "prod" over the model-supplied input,
// which matched "product" and, worse, missed every native k8s_* call made
// against sre.kube_context="prod-eks" because the context never appears in
// the input.
func ResolveEnvironment(tool string, input map[string]any, sre config.SreConfig) Environment {
	var signals []string
	collectStrings(input, &signals)
	switch {
	case strings.HasPrefix(tool, "k8s_"):
		if sre.KubeContext != "" {
			signals = append(signals, sre.KubeContext)
		}
	case tool == "cloud_read":
		if sre.Cloud.Profile != "" {
			signals = append(signals, sre.Cloud.Profile)
		}
	}

	tokens := exactTokens(signals)
	for _, env := range sre.Environments {
		if matched, via := matchEnvironment(env, tokens); matched {
			return Environment{Name: env.Name, Tier: normalizeTier(env.Tier), Reason: "declared environment matched " + via}
		}
	}

	tier, word := "", ""
	for _, w := range words(signals) {
		if prodWords[w] {
			return Environment{Tier: config.TierProd, Reason: "keyword " + w}
		}
		if t, ok := nonProdWords[w]; ok && tier == "" {
			tier, word = t, w
		}
	}
	if tier != "" {
		return Environment{Tier: tier, Reason: "keyword " + word}
	}
	return Environment{Tier: config.TierUnknown, Reason: "no declared environment or keyword matched"}
}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case config.TierProd, "production":
		return config.TierProd
	case config.TierStaging:
		return config.TierStaging
	case config.TierDev:
		return config.TierDev
	default:
		// A declared environment with a typo'd tier must not silently
		// downgrade to non-production.
		return config.TierUnknown
	}
}

func matchEnvironment(env config.EnvironmentConfig, tokens []string) (bool, string) {
	for _, token := range tokens {
		for _, list := range [][]string{env.KubeContexts, env.CloudProfiles, env.TerraformWorkspaces} {
			for _, id := range list {
				if id != "" && token == id {
					return true, id
				}
			}
		}
		normalized := filepath.ToSlash(token)
		for _, path := range env.Paths {
			path = strings.TrimSuffix(filepath.ToSlash(path), "/")
			if path == "" {
				continue
			}
			if normalized == path || strings.HasPrefix(normalized, path+"/") || strings.Contains(normalized, "/"+path+"/") || strings.HasSuffix(normalized, "/"+path) {
				return true, path
			}
		}
	}
	return false, ""
}

func collectStrings(value any, out *[]string) {
	switch v := value.(type) {
	case string:
		*out = append(*out, v)
	case []any:
		for _, item := range v {
			collectStrings(item, out)
		}
	case []string:
		*out = append(*out, v...)
	case map[string]any:
		for _, item := range v {
			collectStrings(item, out)
		}
	}
}

// exactTokens splits on whitespace, quotes and "=" so that
// "--context=prod-eks", "--profile prod" and "-chdir=infra/prod" all yield
// the bare identifier.
func exactTokens(signals []string) []string {
	var tokens []string
	for _, s := range signals {
		tokens = append(tokens, strings.FieldsFunc(s, func(r rune) bool {
			return unicode.IsSpace(r) || r == '=' || r == '"' || r == '\'' || r == ','
		})...)
	}
	return tokens
}

// words splits on every non-alphanumeric rune and lowercases, so
// "aws_ebs_volume.production_db" yields "production" but "product" never
// yields "prod".
func words(signals []string) []string {
	var out []string
	for _, s := range signals {
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			out = append(out, w)
		}
	}
	return out
}
