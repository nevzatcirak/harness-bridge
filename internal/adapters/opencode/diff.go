package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

// FileDiff is one item of GET /session/:id/diff and GET /vcs/diff (upstream
// SnapshotFileDiff / VcsFileDiff, ADR-023 section 1.2 "Recorded response shapes").
// File and Patch are pointers: the upstream schema makes them optional on the
// session route, and an absent patch is not an empty patch. The shapes are read
// from the upstream source, never captured from a live server (UNVERIFIED).
type FileDiff struct {
	File      *string `json:"file,omitempty"`
	Patch     *string `json:"patch,omitempty"`
	Additions float64 `json:"additions"`
	Deletions float64 `json:"deletions"`
	Status    string  `json:"status,omitempty"`
}

// DiffState is what a user message says about its per-turn diff
// (info.summary.diffs). It exists so that "not computed" can never be mistaken
// for "computed, no changes".
type DiffState string

const (
	// DiffNotComputed: info.summary is absent, or present without diffs, or
	// diffs is null or not an array. Never shown as "no changes".
	DiffNotComputed DiffState = "not_computed"
	// DiffComputedEmpty: info.summary.diffs is the empty array.
	DiffComputedEmpty DiffState = "computed_empty"
	// DiffChanges: info.summary.diffs has at least one item.
	DiffChanges DiffState = "changes"
)

// DiffState reads the presence rule from the raw info.summary of a message. It
// never fails the message: any shape it does not understand is "not computed".
func (i MessageInfo) DiffState() DiffState {
	if len(i.Summary) == 0 {
		return DiffNotComputed
	}
	var s struct {
		Diffs json.RawMessage `json:"diffs"`
	}
	if json.Unmarshal(i.Summary, &s) != nil || len(s.Diffs) == 0 || string(s.Diffs) == "null" {
		return DiffNotComputed
	}
	var items []json.RawMessage
	if json.Unmarshal(s.Diffs, &items) != nil {
		return DiffNotComputed
	}
	if len(items) == 0 {
		return DiffComputedEmpty
	}
	return DiffChanges
}

// VCS diff modes (upstream Vcs.Mode).
const (
	VCSModeGit    = "git"
	VCSModeBranch = "branch"
)

// SessionDiff reads GET /session/:id/diff?messageID=<id>: the per-turn diff of
// one user message. Read-only; the response bound of the client (8 MiB) applies.
func (c *Client) SessionDiff(ctx context.Context, id, messageID string) ([]FileDiff, error) {
	if !ValidID(id, "ses") || !ValidID(messageID, "msg") {
		return nil, errors.New("invalid native session or message ID")
	}
	var out []FileDiff
	q := url.Values{"messageID": {messageID}}
	if err := c.do(ctx, "GET", "/session/"+id+"/diff?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// VCSDiff reads GET /vcs/diff?mode=<git|branch>[&context=N]: the working-tree
// diff of the client's directory. context < 0 means "not sent". Read-only. An
// answer larger than the client bound (8 MiB) fails; the native cap allows more.
func (c *Client) VCSDiff(ctx context.Context, mode string, context_ int) ([]FileDiff, error) {
	if mode != VCSModeGit && mode != VCSModeBranch {
		return nil, errors.New("invalid vcs diff mode")
	}
	q := url.Values{"mode": {mode}}
	if context_ >= 0 {
		q.Set("context", strconv.Itoa(context_))
	}
	var out []FileDiff
	if err := c.do(ctx, "GET", "/vcs/diff?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
