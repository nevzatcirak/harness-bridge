package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func TestHTTPSListenerRejectsPlaintextAndUntrustedCertificate(t *testing.T) {
	s := &Server{Token: strings.Repeat("t", 32)}
	ts := httptest.NewTLSServer(s)
	defer ts.Close()
	var err error
	s.Remote, err = security.NewRemote(ts.URL, "operator", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// The real TLS listener rejects plaintext before authentication/SDK dispatch.
	r, err := http.Get("http" + strings.TrimPrefix(ts.URL, "https") + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatal("plaintext accepted", r.StatusCode)
	}
	// A normal client must not trust the fixture certificate without its CA.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: time.Second}
	if response, err := c.Get(ts.URL + "/mcp"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
}

func TestHTTPSAuthenticationDenial(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "expired", "duplicate", "origin", "host", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s := &Server{Token: strings.Repeat("t", 32)}
			ts := httptest.NewTLSServer(s)
			defer ts.Close()
			var err error
			// Constructor refuses expired credentials. Let a short-lived credential expire.
			expiry := time.Now().Add(time.Hour)
			if kind == "expired" {
				expiry = time.Now().Add(time.Millisecond * 20)
			}
			s.Remote, err = security.NewRemote(ts.URL, "operator", expiry)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "expired" {
				time.Sleep(time.Millisecond * 25)
			}
			body := `{ "jsonrpc":"2.0", "id":1, "method":"ping" }`
			want := http.StatusUnauthorized
			if kind == "oversized" {
				body = strings.Repeat("x", (1<<20)+1)
				want = 413
			}
			r, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+s.Token)
			switch kind {
			case "missing":
				r.Header.Del("Authorization")
			case "invalid":
				r.Header.Set("Authorization", "Bearer wrong")
			case "duplicate":
				r.Header.Add("Authorization", "Bearer "+s.Token)
			case "origin":
				r.Header.Set("Origin", "https://evil.example")
				want = 403
			case "host":
				r.Host = "evil.example"
				want = 403
			}
			response, err := ts.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != want {
				t.Fatalf("got %d want %d", response.StatusCode, want)
			}
			if want == 401 && response.Header.Get("WWW-Authenticate") == "" {
				t.Fatal("missing challenge")
			}
		})
	}
}
