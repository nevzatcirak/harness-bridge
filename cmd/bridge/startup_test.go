package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const (
	token32 = "abcdefghijklmnopqrstuvwxyz012345" // 32 characters
	token43 = token32 + "ZQ9XK2MVB7R"            // 43 characters
)

func TestStartupTokenLength(t *testing.T) {
	if len(token32) != 32 || len(token43) != 43 {
		t.Fatal("fixture lengths")
	}
	now := time.Now()
	// Up to 24h the existing 32-character minimum applies, with its old text.
	if _, err := loadIngress(remoteEnv(now.Add(time.Hour), "", false), token32); err != nil {
		t.Fatal("default, 32 characters:", err)
	}
	_, err := loadIngress(remoteEnv(now.Add(time.Hour), "", false), token32[:31])
	if err == nil || err.Error() != "BRIDGE_TOKEN must be at least 32 characters" {
		t.Fatalf("default, 31 characters: %v", err)
	}
	if _, err := loadIngress(remoteEnv(now.Add(time.Hour), "24h", true), token32); err != nil {
		t.Fatal("24h, 32 characters:", err)
	}
	// Above 24h a long-lived token must be at least 43 characters.
	for _, v := range []string{"25h", "168h", "720h"} {
		_, err := loadIngress(remoteEnv(now.Add(time.Hour), v, true), token43[:42])
		if err == nil || err.Error() != "BRIDGE_TOKEN must be at least 43 characters" {
			t.Fatalf("%s, 42 characters: %v", v, err)
		}
		if _, err := loadIngress(remoteEnv(now.Add(time.Hour), v, true), token43); err != nil {
			t.Fatalf("%s, 43 characters: %v", v, err)
		}
	}
	// Local mode uses the same rule for the configured lifetime.
	local := func(k string) string {
		if k == "BRIDGE_TOKEN_MAX_LIFETIME" {
			return "168h"
		}
		return ""
	}
	if _, err := loadIngress(local, token32); err == nil {
		t.Fatal("local mode, 168h, 32 characters accepted")
	}
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestStartupLogHasNoTokenMaterial(t *testing.T) {
	buf := captureSlog(t)
	expires := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	if _, err := loadIngress(remoteEnv(expires, "168h", true), token43); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "remote token lifetime") != 1 {
		t.Fatalf("want exactly one startup line, got %q", out)
	}
	for _, want := range []string{"token_expires=" + expires.Format(time.RFC3339), "token_max_lifetime=168h0m0s", "principal=operator"} {
		if !strings.Contains(out, want) {
			t.Errorf("startup log lacks %q: %q", want, out)
		}
	}
	sum := sha256.Sum256([]byte(token43))
	digest := hex.EncodeToString(sum[:])
	for _, secret := range []string{token43, token43[:8], token43[len(token43)-8:], digest, digest[:12]} {
		if strings.Contains(out, secret) {
			t.Fatalf("startup log contains token material %q: %q", secret, out)
		}
	}
	// Local mode logs nothing about token lifetime.
	buf.Reset()
	if _, err := loadIngress(func(string) string { return "" }, token32); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "remote token lifetime") {
		t.Fatalf("local mode logged the lifetime: %q", buf.String())
	}
}
