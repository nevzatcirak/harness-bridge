package opencode

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"unicode"
)

// ADR-022 slice S4: the two native calls of create_session. Both are bounded by the caller's
// context; neither is ever retried here.

// CreateBodyMaxTitle bounds the title the client will send.
const CreateBodyMaxTitle = 200

// createBody is the ENTIRE body of a create. It has no field for permission, agent, model,
// parentID or workspaceID, so no caller can send one (ADR-022 4.1 step 4): the Bridge never lets
// Chat pre-set a permission ruleset.
type createBody struct {
	Title    string            `json:"title,omitempty"`
	Metadata map[string]string `json:"metadata"`
}

// IsDefiniteRefusal reports whether err is an HTTP status answer (4xx or 5xx) of the native
// server, as opposed to a transport failure, a timeout or an unreadable answer, after which the
// outcome of a write is unknown.
func IsDefiniteRefusal(err error) bool {
	var h *HTTPError
	return errors.As(err, &h)
}

// CreateSession sends POST /session?directory=<directory> with {title?, metadata}. directory
// must be an absolute clean path; the native server decides the new session's directory from
// it (ADR-022 section 2). A non-nil error that is not a definite refusal (IsDefiniteRefusal)
// means the session may exist.
func (c *Client) CreateSession(ctx context.Context, directory, title string, metadata map[string]string) (Session, error) {
	var s Session
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return s, errors.New("invalid directory for a create")
	}
	if len([]rune(title)) > CreateBodyMaxTitle {
		return s, errors.New("title too long")
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return s, errors.New("title has a control character")
		}
	}
	if len(metadata) > 8 {
		return s, errors.New("too many metadata keys")
	}
	err := c.WithDirectory(directory).do(ctx, "POST", "/session", createBody{Title: title, Metadata: metadata}, &s)
	return s, err
}

// ListDirectory reads the sessions the native server lists for directory (the default list:
// archived sessions are not in it), asking for at most limit entries. It is the bounded
// catalog read behind the create caps (ADR-022 3.3); the caller treats an answer with more
// entries than it asked for as truncated.
func (c *Client) ListDirectory(ctx context.Context, directory string, limit int) ([]Session, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || limit < 1 || limit > 100000 {
		return nil, errors.New("invalid list request")
	}
	var s []Session
	err := c.WithDirectory(directory).do(ctx, "GET", "/session?limit="+strconv.Itoa(limit), nil, &s)
	return s, err
}
