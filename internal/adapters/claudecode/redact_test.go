package claudecode

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for name, in := range map[string]string{
		"anthropic key":   "key is sk-ant-api03-AbCdEf_123-xyz ok",
		"anthropic oauth": "tok sk-ant-oat01-AbCdEf123456789",
		"bearer":          "Authorization: Bearer abcDEF123.456_-x==",
		"auth header":     "authorization: Basic dXNlcjpwYXNz",
		"private key":     "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----",
		"openssh key":     "-----BEGIN OPENSSH PRIVATE KEY-----\nxx\n-----END OPENSSH PRIVATE KEY-----",
		"bridge var":      "export BRIDGE_TOKEN=supersecretvalue",
		"anthropic var":   "ANTHROPIC_API_KEY=abc123",
		"opencode var":    "OPENCODE_SERVER_PASSWORD='p w'",
		"bridge quoted":   `BRIDGE_ADMIN_SOCKET="/run/x y"`,
	} {
		out := Redact(in)
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: nothing redacted: %q", name, out)
		}
		for _, secret := range []string{"AbCdEf", "abcDEF123", "dXNlcjpwYXNz", "MIIEow", "supersecretvalue", "abc123", "p w", "/run/x y"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s: secret %q survived: %q", name, secret, out)
			}
		}
	}
	if got := Redact("plain text without secrets, BRIDGE is a word"); got != "plain text without secrets, BRIDGE is a word" {
		t.Errorf("clean text changed: %q", got)
	}
	if RedactionRulesVersion != 1 {
		t.Errorf("rule set version = %d", RedactionRulesVersion)
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	in := "sk-ant-api03-ABCDEFGHIJ Bearer tok123 BRIDGE_X=1"
	once := Redact(in)
	if twice := Redact(once); twice != once {
		t.Fatalf("not idempotent: %q vs %q", once, twice)
	}
}
