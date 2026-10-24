package security

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteBoundary(t *testing.T) {
	p, err := NewRemote("https://bridge.example", "operator", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "plaintext", "host", "origin", "duplicate-origin", "query", "expired", "forwarded"} {
		t.Run(kind, func(t *testing.T) {
			cp := *p
			r := httptest.NewRequest("POST", "https://bridge.example/mcp", nil)
			r.TLS = &tls.ConnectionState{}
			switch kind {
			case "plaintext":
				r.TLS = nil
			case "host":
				r.Host = "evil.example"
			case "origin":
				r.Header.Set("Origin", "https://evil.example")
			case "duplicate-origin":
				r.Header.Add("Origin", p.origin)
				r.Header.Add("Origin", p.origin)
			case "query":
				r.URL.RawQuery = "access_token=secret"
			case "expired":
				cp.expires = time.Now().Add(-time.Second)
			case "forwarded":
				r.TLS = nil
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-Host", p.host)
			}
			w := httptest.NewRecorder()
			if cp.Check(w, r) != (kind == "valid") {
				t.Fatal("boundary bypass", kind)
			}
		})
	}
}

func TestRemoteConfiguration(t *testing.T) {
	for _, origin := range []string{"http://bridge.example", "https://u:p@bridge.example", "https://bridge.example/", "https://bridge.example?", "https://bridge.example#x"} {
		if _, err := NewRemote(origin, "operator", time.Now().Add(time.Hour)); err == nil {
			t.Fatal(origin)
		}
	}
	for _, expiry := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(25 * time.Hour)} {
		if _, err := NewRemote("https://bridge.example", "operator", expiry); err == nil {
			t.Fatal("expiry accepted")
		}
	}
}
