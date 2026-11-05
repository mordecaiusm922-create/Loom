package providers

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// apiKeyFromEnv reads an API key from the environment. Surrounding
// whitespace is trimmed: a pasted key often carries a trailing newline or
// space. Any other character that cannot appear in an HTTP header is
// reported without echoing the key, instead of failing deep inside net/http.
func apiKeyFromEnv(name string) (string, error) {
	key := strings.TrimSpace(os.Getenv(name))
	if key == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("%s contains characters that are not valid in an API key, usually from pasting it; set it again", name)
		}
	}
	return key, nil
}

// rejectedKeyError explains an HTTP 401/403 from a model provider. The
// response body is left out on purpose: it adds nothing actionable and some
// providers echo request details back.
func rejectedKeyError(provider, envName string, status int) error {
	return fmt.Errorf("%s rejected the API key in %s (HTTP %d): check that it is the complete key for this provider and that it has not been revoked", provider, envName, status)
}

// CheckAPIKey reports whether the API key in the environment variable name
// can be sent. It never returns the key itself.
func CheckAPIKey(name string) error {
	_, err := apiKeyFromEnv(name)
	return err
}

// knownKeyPrefixes maps provider hosts to the prefix their API keys start
// with, so a key copied for the wrong provider (or a key that is not a key
// at all) is caught before the first request.
var knownKeyPrefixes = map[string]string{
	"api.anthropic.com":                 "sk-ant-",
	"openrouter.ai":                     "sk-or-",
	"api.openai.com":                    "sk-",
	"api.groq.com":                      "gsk_",
	"generativelanguage.googleapis.com": "AIza",
}

// ExpectedKeyPrefix returns the usual key prefix for a provider base URL, or
// "" when the provider is not known.
func ExpectedKeyPrefix(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return knownKeyPrefixes[strings.ToLower(u.Hostname())]
}

// KeyHasPrefix reports whether the key in name starts with prefix, without
// exposing the key.
func KeyHasPrefix(name, prefix string) bool {
	return strings.HasPrefix(strings.TrimSpace(os.Getenv(name)), prefix)
}
