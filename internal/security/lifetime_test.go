package security

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ADR-019 option (2): configurable maximum token lifetime with a 720h ceiling.

func TestParseTokenMaxLifetime(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"":     DefaultTokenMaxLifetime,
		"24h":  24 * time.Hour,
		"1h":   time.Hour,
		"168h": 168 * time.Hour,
		"720h": TokenMaxLifetimeCeiling,
	} {
		got, err := ParseTokenMaxLifetime(v)
		if err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"0", "0s", "-1h", "garbage", "1d", "24", "721h", "720h0m1s", "8760h"} {
		if got, err := ParseTokenMaxLifetime(v); err == nil {
			t.Errorf("%q accepted as %v", v, got)
		}
	}
	if DefaultTokenMaxLifetime != 24*time.Hour || TokenMaxLifetimeCeiling != 720*time.Hour {
		t.Fatal("default or ceiling changed")
	}
}

func TestNewRemoteWithMaxLifetime(t *testing.T) {
	now := time.Now()
	ok := func(expires time.Time, d time.Duration) error {
		_, err := NewRemoteWithMaxLifetime("https://bridge.example", "operator", expires, d)
		return err
	}
	if err := ok(now.Add(7*24*time.Hour), 168*time.Hour); err != nil {
		t.Fatal("7-day expiry under 168h:", err)
	}
	if err := ok(now.Add(7*24*time.Hour+time.Minute), 168*time.Hour); err == nil {
		t.Fatal("7 days + 1 minute accepted under 168h")
	}
	if err := ok(now.Add(720*time.Hour), TokenMaxLifetimeCeiling); err != nil {
		t.Fatal("30-day expiry under 720h:", err)
	}
	// The constructor enforces the ceiling itself, not only the env parser.
	for _, d := range []time.Duration{0, -time.Hour, TokenMaxLifetimeCeiling + time.Second} {
		if err := ok(now.Add(time.Hour), d); err == nil {
			t.Fatalf("max lifetime %v accepted", d)
		}
	}
	if err := ok(now.Add(-time.Second), 168*time.Hour); err == nil {
		t.Fatal("past expiry accepted")
	}
	// The default path keeps the exact 24 h behavior and error text.
	_, err := NewRemote("https://bridge.example", "operator", now.Add(25*time.Hour))
	if err == nil || err.Error() != "named principal and future token expiry within 24 hours required" {
		t.Fatalf("default error changed: %v", err)
	}
	_, err = NewRemoteWithMaxLifetime("https://bridge.example", "operator", now.Add(200*time.Hour), 168*time.Hour)
	if err == nil || !strings.Contains(err.Error(), "BRIDGE_TOKEN_MAX_LIFETIME (168h0m0s)") {
		t.Fatalf("lifetime error: %v", err)
	}
}

// A long-lived configuration does not weaken the per-request expiry check.
func TestLongLifetimeStillExpiresPerRequest(t *testing.T) {
	p, err := NewRemoteWithMaxLifetime("https://bridge.example", "operator", time.Now().Add(168*time.Hour), 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, expired := range []bool{false, true} {
		cp := *p
		if expired {
			cp.expires = time.Now().Add(-time.Second)
		}
		r := httptest.NewRequest("POST", "https://bridge.example/mcp", nil)
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		if got := cp.Check(w, r); got == expired {
			t.Fatalf("expired=%v: Check = %v", expired, got)
		}
		if expired && (w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`)) {
			t.Fatalf("expired: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestTokenMinLength(t *testing.T) {
	for d, want := range map[time.Duration]int{
		time.Hour:                  32,
		24 * time.Hour:             32,
		24*time.Hour + time.Second: 43,
		168 * time.Hour:            43,
		TokenMaxLifetimeCeiling:    43,
	} {
		if got := TokenMinLength(d); got != want {
			t.Errorf("TokenMinLength(%v) = %d, want %d", d, got, want)
		}
	}
}
