package redact

import (
	"strings"
	"testing"
)

func TestStringMasksKnownCredentialShapes(t *testing.T) {
	secrets := map[string]string{
		"aws key id":      "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		"aws secret":      "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"jwt":             "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"github":          "GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"slack":           "hook xoxb-123456789012-abcdefghijkl",
		"bearer":          "Authorization: Bearer abcdef0123456789abcdef0123456789",
		"url creds":       "DATABASE_URL=postgres://app:S3cr3tPassw0rd@db.internal:5432/app",
		"password yaml":   "  password: hunter2hunter2",
		"password json":   `{"db_password": "hunter2hunter2"}`,
		"vault token":     "VAULT_TOKEN=hvs.CAESIJlU2bFz1234567890abcdefghij",
		"anthropic key":   "ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"connection str":  "connection_string=Server=tcp:x.database.windows.net;Password=abcdef123",
		"private key pem": "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234\nabcd\n-----END RSA PRIVATE KEY-----",
	}
	leaks := []string{"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI", "dozjgNryP4J3", "ghp_abcdef", "xoxb-1234", "abcdef0123456789abcdef", "S3cr3tPassw0rd", "hunter2hunter2", "hvs.CAES", "sk-ant-api03", "MIIEowIBAAKCAQEA"}
	for name, input := range secrets {
		out := String(input)
		if !strings.Contains(out, Mask) {
			t.Errorf("%s: nothing masked in %q", name, out)
		}
		for _, leak := range leaks {
			if strings.Contains(input, leak) && strings.Contains(out, leak) {
				t.Errorf("%s: %q still present in %q", name, leak, out)
			}
		}
	}
}

func TestStringLeavesOrdinaryOutputAlone(t *testing.T) {
	ordinary := []string{
		"NAME        READY   STATUS    RESTARTS   AGE\napi-0       1/1     Running   0          3d",
		"aws_db_instance.main: Refreshing state... [id=prod-db]",
		"token: 5",
		"secret_count: 3",
		"Plan: 1 to add, 0 to change, 1 to destroy.",
		"https://grafana.internal/d/abc?orgId=1",
	}
	for _, input := range ordinary {
		if out := String(input); out != input {
			t.Errorf("changed ordinary output:\n in: %q\nout: %q", input, out)
		}
	}
}

// Secret keys are arbitrary names, so only the Secret-aware pass can mask
// them; base64 in data: is encoding, not protection.
func TestStringMasksKubernetesSecretDataYAMLAndJSON(t *testing.T) {
	yaml := "apiVersion: v1\ndata:\n  DB_URL: cG9zdGdyZXM6Ly9hcHA6cGFzc0BkYg==\n  tls.crt: LS0tLS1CRUdJTg==\nkind: Secret\nmetadata:\n  name: api\n"
	out := String(yaml)
	if strings.Contains(out, "cG9zdGdyZXM6") || strings.Contains(out, "LS0tLS1CRUdJTg") {
		t.Fatalf("secret data leaked:\n%s", out)
	}
	if !strings.Contains(out, "name: api") || !strings.Contains(out, "DB_URL: "+Mask) {
		t.Fatalf("structure not preserved:\n%s", out)
	}

	json := "{\n    \"apiVersion\": \"v1\",\n    \"data\": {\n        \"DB_URL\": \"cG9zdGdyZXM6Ly9hcHA6cGFzc0BkYg==\"\n    },\n    \"kind\": \"Secret\",\n    \"metadata\": {\n        \"name\": \"api\"\n    }\n}"
	out = String(json)
	if strings.Contains(out, "cG9zdGdyZXM6") {
		t.Fatalf("secret data leaked:\n%s", out)
	}
	if !strings.Contains(out, `"name": "api"`) {
		t.Fatalf("metadata was masked too:\n%s", out)
	}
}

// ConfigMaps are not secrets: their data must stay readable for debugging.
func TestStringLeavesConfigMapDataAlone(t *testing.T) {
	cm := "apiVersion: v1\ndata:\n  LOG_LEVEL: debug\nkind: ConfigMap\n"
	if out := String(cm); out != cm {
		t.Fatalf("ConfigMap changed:\n%s", out)
	}
}
