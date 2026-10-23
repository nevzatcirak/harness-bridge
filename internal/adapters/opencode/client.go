// Package opencode connects only to an already running loopback server.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	ProjectID string `json:"projectID"`
}
type MessageInfo struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	SessionID string          `json:"sessionID"`
	ParentID  string          `json:"parentID,omitempty"`
	Finish    string          `json:"finish,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
	Time      struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed,omitempty"`
	} `json:"time"`
	Path struct {
		Cwd  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path,omitempty"`
}
type Message struct {
	Info  MessageInfo       `json:"info"`
	Parts []json.RawMessage `json:"parts"`
}
type Status struct {
	Type string `json:"type"`
}
type Permission struct {
	SessionID string `json:"sessionID"`
}
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("opencode HTTP %d", e.Status) }

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func ValidID(id, prefix string) bool {
	return len(id) > len(prefix) && len(id) <= 256 && strings.HasPrefix(id, prefix) && identifier.MatchString(id)
}

type Client struct {
	base                          *url.URL
	http                          *http.Client
	username, password, directory string
}

func New(base, username, password string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, errors.New("invalid OpenCode URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("invalid OpenCode URL scheme")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("unexpected URL components")
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return nil, errors.New("OpenCode must use loopback")
	}
	if username == "" {
		username = "opencode"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{base: u, http: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, username: username, password: password}, nil
}

// WithDirectory routes every request to the native session's original workspace.
func (c *Client) WithDirectory(directory string) *Client { v := *c; v.directory = directory; return &v }
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	u := *c.base
	u.Path = path
	if c.directory != "" {
		q := u.Query()
		q.Set("directory", c.directory)
		u.RawQuery = q.Encode()
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("opencode transport unavailable or request canceled")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{Status: res.StatusCode}
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return errors.New("opencode response read failed")
	}
	if len(data) > 8<<20 {
		return errors.New("opencode response exceeds 8 MiB")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return errors.New("invalid opencode JSON response")
	}
	return nil
}
func (c *Client) List(ctx context.Context) ([]Session, error) {
	var s []Session
	e := c.do(ctx, "GET", "/session", nil, &s)
	return s, e
}
func (c *Client) Get(ctx context.Context, id string) (Session, error) {
	var s Session
	if !ValidID(id, "ses") {
		return s, errors.New("invalid native session ID")
	}
	e := c.do(ctx, "GET", "/session/"+id, nil, &s)
	return s, e
}
func (c *Client) History(ctx context.Context, id string) ([]Message, error) {
	if !ValidID(id, "ses") {
		return nil, errors.New("invalid native session ID")
	}
	var m []Message
	e := c.do(ctx, "GET", "/session/"+id+"/message", nil, &m)
	return m, e
}
func (c *Client) Status(ctx context.Context) (map[string]Status, error) {
	var s map[string]Status
	e := c.do(ctx, "GET", "/session/status", nil, &s)
	return s, e
}
func (c *Client) Permissions(ctx context.Context) ([]Permission, error) {
	var p []Permission
	e := c.do(ctx, "GET", "/permission", nil, &p)
	return p, e
}
func (c *Client) Prompt(ctx context.Context, id, prompt, messageID string) error {
	if !ValidID(id, "ses") || !ValidID(messageID, "msg") {
		return errors.New("invalid native ID")
	}
	return c.do(ctx, "POST", "/session/"+id+"/prompt_async", map[string]any{"messageID": messageID, "parts": []any{map[string]string{"type": "text", "text": prompt}}}, nil)
}
func (c *Client) Abort(ctx context.Context, id string) error {
	if !ValidID(id, "ses") {
		return errors.New("invalid native session ID")
	}
	var acknowledged bool
	if e := c.do(ctx, "POST", "/session/"+id+"/abort", nil, &acknowledged); e != nil {
		return e
	}
	if !acknowledged {
		return errors.New("abort rejected")
	}
	return nil
}
