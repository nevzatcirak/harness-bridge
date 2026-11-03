package mcpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/netip"
	"net/url"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ConnectPrivateTLS pins the network destination to a private/loopback IP while
// independently verifying the public certificate name and exact HTTP Host.
// This is a staging probe, never a TLS bypass or a public routing client.
func ConnectPrivateTLS(ctx context.Context, endpoint, origin, token string, caPEM []byte) (*sdk.ClientSession, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "/mcp" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, errors.New("private HTTPS /mcp endpoint required")
	}
	if _, err = netip.ParseAddr(u.Hostname()); err != nil {
		return nil, errors.New("literal private/loopback IP required")
	}
	if err = security.ValidateListen(u.Host, true); err != nil {
		return nil, err
	}
	public, err := url.Parse(origin)
	if err != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.Path != "" || public.RawQuery != "" || public.Fragment != "" || public.ForceQuery || token == "" {
		return nil, errors.New("public HTTPS origin and token required")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("valid public CA certificate required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: public.Hostname()}
	client := &http.Client{Transport: privateBearer{token: token, host: public.Host, base: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return sdk.NewClient(&sdk.Implementation{Name: "harness-bridge-private-probe", Version: "0.3.0"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}

type privateBearer struct {
	token, host string
	base        http.RoundTripper
}

func (b privateBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = b.host
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
