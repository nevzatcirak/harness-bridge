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
	if RedactionRulesVersion != 2 {
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

// A private-key block whose END line is missing (a truncated tool result, a
// record cut at the record bound, a block split across lines) is redacted from
// its BEGIN line to the end of the text (#218 P3-5).
func TestRedactUnterminatedPrivateKey(t *testing.T) {
	for name, in := range map[string]string{
		"no end line":       "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabcdef",
		"openssh no end":    "x -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk",
		"mismatched end":    "-----BEGIN PRIVATE KEY-----\nKEYBODY\n-----END CERTIFICATE-----",
		"two blocks 2 open": "-----BEGIN EC PRIVATE KEY-----\nAAA\n-----END EC PRIVATE KEY-----\n-----BEGIN PRIVATE KEY-----\nTAILBODY",
	} {
		out := Redact(in)
		for _, secret := range []string{"MIIEow", "abcdef", "b3BlbnNzaC1", "KEYBODY", "AAA", "TAILBODY"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s: %q survived: %q", name, secret, out)
			}
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: nothing redacted: %q", name, out)
		}
	}
	if got := Redact("before\n-----BEGIN RSA PRIVATE KEY-----\nabc"); !strings.HasPrefix(got, "before\n") {
		t.Errorf("text before the block must be kept: %q", got)
	}
	if got := Redact("a PUBLIC KEY block\n-----BEGIN PUBLIC KEY-----\nkeep\n"); strings.Contains(got, "REDACTED") {
		t.Errorf("a public key is not a secret: %q", got)
	}
}
