// Package mcpclient supplies an authenticated, loopback-only official SDK client.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
func Connect(ctx context.Context, endpoint, token string) (*sdk.ClientSession, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && u.Hostname() != "localhost") || token == "" {
		return nil, errors.New("authenticated loopback HTTP endpoint required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: bearerTransport{token, transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return sdk.NewClient(&sdk.Implementation{Name: "harness-bridge-local-client", Version: "0.2.0"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}
func Call[T any](ctx context.Context, client *sdk.ClientSession, name string, args any) (T, error) {
	var out T
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return out, err
	}
	if result.IsError {
		return out, errors.New("MCP tool returned an error; do not retry a mutation blindly")
	}
	b, err := json.Marshal(result.StructuredContent)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}
