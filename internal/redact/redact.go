// Package redact masks credentials in text before it leaves the machine.
// Every tool output passes through String before it is appended to the
// conversation, because the conversation is sent verbatim to whichever
// model provider the route selected -- a third party.
package redact

import (
	"regexp"
	"strings"
)

// Mask replaces a secret value. It is fixed-width on purpose: its length
// must not reveal the secret's length.
const Mask = "[REDACTED]"

type rule struct {
	re *regexp.Regexp
	// group is the capture group to mask; 0 masks the whole match.
	group int
}

var rules = []rule{
	{re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[0-9A-Z]{16}\b`)},
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`)},
	{re: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{re: regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{re: regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`)},
	{re: regexp.MustCompile(`\bhvs\.[A-Za-z0-9_-]{20,}`)},
	{re: regexp.MustCompile(`(?i)\b(bearer\s+)([A-Za-z0-9._~+/=-]{16,})`), group: 2},
	// URL credentials: scheme://user:password@host
	{re: regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:)([^\s@/]{3,})(@)`), group: 2},
	// key = value / key: value / "key": "value" for credential-shaped keys.
	{re: regexp.MustCompile(`(?i)((?:[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|connection[_-]?string))["']?\s*[:=]\s*["']?)([^\s"',;}{]{6,})`), group: 2},
}

// String masks every credential-shaped value in s.
func String(s string) string {
	if s == "" {
		return s
	}
	for _, r := range rules {
		if r.group == 0 {
			s = r.re.ReplaceAllString(s, Mask)
			continue
		}
		s = r.re.ReplaceAllStringFunc(s, func(match string) string {
			idx := r.re.FindStringSubmatchIndex(match)
			if idx == nil || idx[2*r.group] < 0 {
				return match
			}
			return match[:idx[2*r.group]] + Mask + match[idx[2*r.group+1]:]
		})
	}
	if looksLikeK8sSecret(s) {
		s = maskSecretData(s)
	}
	return s
}

var k8sSecretKind = regexp.MustCompile(`(?m)(?:^\s*kind:\s*Secret\s*$|"kind":\s*"Secret")`)

func looksLikeK8sSecret(s string) bool { return k8sSecretKind.MatchString(s) }

var (
	yamlDataHeader = regexp.MustCompile(`^(\s*)(?:data|stringData):\s*$`)
	yamlKeyValue   = regexp.MustCompile(`^(\s*[^\s:#][^:]*:\s*)(\S.*)$`)
	jsonDataHeader = regexp.MustCompile(`"(?:data|stringData)":\s*\{\s*$`)
	jsonKeyValue   = regexp.MustCompile(`^(\s*"[^"]+":\s*)"[^"]*"(,?)\s*$`)
)

// maskSecretData blanks every value under data:/stringData: in a
// Kubernetes Secret rendered as YAML or indented JSON (kubectl -o yaml|json).
// Secret keys are arbitrary ("DB_URL", "tls.crt"), so the key-name rule
// above cannot catch them, and base64 is encoding, not protection.
func maskSecretData(s string) string {
	lines := strings.Split(s, "\n")
	inYAML, yamlIndent := false, 0
	inJSON := false
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case inJSON:
			if strings.HasPrefix(strings.TrimSpace(trimmed), "}") {
				inJSON = false
				continue
			}
			if m := jsonKeyValue.FindStringSubmatch(trimmed); m != nil {
				lines[i] = m[1] + `"` + Mask + `"` + m[2]
			}
		case inYAML:
			indent := len(trimmed) - len(strings.TrimLeft(trimmed, " \t"))
			if strings.TrimSpace(trimmed) != "" && indent <= yamlIndent {
				inYAML = false
			} else if m := yamlKeyValue.FindStringSubmatch(trimmed); m != nil {
				lines[i] = m[1] + Mask
				continue
			} else {
				continue
			}
		}
		if inYAML || inJSON {
			continue
		}
		if m := yamlDataHeader.FindStringSubmatch(trimmed); m != nil {
			inYAML, yamlIndent = true, len(m[1])
		} else if jsonDataHeader.MatchString(trimmed) {
			inJSON = true
		}
	}
	return strings.Join(lines, "\n")
}
