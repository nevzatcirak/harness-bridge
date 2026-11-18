package opencode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ADR-024 Amendment A1, slice S7 (D21): the Bridge-observed continuity epoch (option (c)).
//
// One long-lived subscription of the Bridge to GET /global/event (one connection for all directories).
// The tracker keeps an epoch token that it generates ITSELF (128 random bits from crypto/rand, never
// derived from an event ID, a message text or any other native value) and replaces it whenever
// continuity of observation may have been lost:
//
//  1. at every (re)connection of the stream, the first included;
//  2. on ANY disposal event, whatever its directory: server.instance.disposed, global.disposed (they are
//     two different event types) and, as a fail-closed superset, any event type that contains "dispos";
//  3. on stream end or error, on an event that cannot be decoded, and when no event has been seen for the
//     heartbeat-gap threshold (25 s, D21 Q5).
//
// The epoch is ARMED only after the first server.heartbeat of a connection (the hello server.connected
// does not arm: the hello is written before the native listener is registered, A1.3(c)(ii)). While it is
// not armed, Epoch returns ("", false) and the seam adapter NativeEpoch returns ("", nil): an unarmed
// tracker is "no evidence", not an error (an error would fail the scope read instead of leaving the
// request unattributed).
//
// What the epoch is: evidence of CONTINUITY OF OBSERVATION, not a native identity (D21 Q1). Event latency
// leaves a window of milliseconds, and a silent middlebox drop one of about 25 s, in which an old epoch is
// still armed although the native instance was replaced (A1.3(c) (i) and (iv)); the checks of ADR-018
// section 5 still have to hold, the epoch only ADDS a condition. Not tested live (UNVERIFIED): the first
// heartbeat time, the exact JSON shape of /global/event (both the {"payload":{"type":...}} envelope and a
// top-level "type" are read), and the behaviour behind a buffering proxy.

const (
	epochGap        = 25 * time.Second
	epochBackoffMin = time.Second
	epochBackoffMax = 30 * time.Second
	epochMaxLine    = 64 << 10 // one SSE line
	epochMaxEvent   = 64 << 10 // the data of one event
	epochMaxType    = 128
)

// EpochClock is the tracker's time source (a manual clock in tests).
type EpochClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realEpochClock struct{}

func (realEpochClock) Now() time.Time                         { return time.Now() }
func (realEpochClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// EpochOpener opens one connection of the global event stream. An error (a refused or non-stream
// answer, 401 included) never arms.
type EpochOpener func(ctx context.Context) (io.ReadCloser, error)

// EpochTrackerConfig configures a tracker. Open is required; Clock and Rand default to the real ones.
type EpochTrackerConfig struct {
	Open  EpochOpener
	Clock EpochClock
	Rand  io.Reader
}

// EpochTracker is the Bridge-observed continuity epoch. The zero value is not usable; a nil *EpochTracker
// answers "unarmed".
type EpochTracker struct {
	open  EpochOpener
	clock EpochClock
	rnd   io.Reader

	mu    sync.Mutex
	token string
	armed bool

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	ctx       context.Context
	done      chan struct{}
}

// NewEpochTracker returns a tracker that is not started.
func NewEpochTracker(cfg EpochTrackerConfig) (*EpochTracker, error) {
	if cfg.Open == nil {
		return nil, errors.New("epoch tracker needs an opener")
	}
	if cfg.Clock == nil {
		cfg.Clock = realEpochClock{}
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &EpochTracker{open: cfg.Open, clock: cfg.Clock, rnd: cfg.Rand, ctx: ctx, cancel: cancel, done: make(chan struct{})}, nil
}

// Start starts the reader goroutine. It is safe to call twice.
func (t *EpochTracker) Start() {
	t.startOnce.Do(func() { go t.run() })
}

// Stop ends the reader, closes the connection and waits for the goroutine. It is idempotent; after Stop
// the epoch is never armed.
func (t *EpochTracker) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		t.cancel()
		t.startOnce.Do(func() { close(t.done) }) // never started: nothing to wait for
	})
	<-t.done
	t.set("", false)
}

// Epoch returns the epoch token and whether it is armed. An unarmed tracker returns ("", false).
func (t *EpochTracker) Epoch() (string, bool) {
	if t == nil {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.armed {
		return "", false
	}
	return t.token, true
}

// NativeEpoch adapts the tracker to the execution seam (execution.NativeIncarnation): the token while
// armed, "" and a nil error otherwise.
func (t *EpochTracker) NativeEpoch(ctx context.Context) (string, error) {
	tok, _ := t.Epoch()
	return tok, nil
}

func (t *EpochTracker) set(token string, armed bool) {
	t.mu.Lock()
	t.token, t.armed = token, armed
	t.mu.Unlock()
}

func (t *EpochTracker) newToken() (string, bool) {
	var b [16]byte
	if _, err := io.ReadFull(t.rnd, b[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}

// run is the reconnect loop: open, read until the stream ends, errors, or the gap rule fires, then wait
// the backoff (1 s doubling to 30 s; a connection that armed resets it to 1 s).
func (t *EpochTracker) run() {
	defer close(t.done)
	backoff := epochBackoffMin
	for t.ctx.Err() == nil {
		armedOnce := t.connection()
		t.set("", false)
		if t.ctx.Err() != nil {
			return
		}
		if armedOnce {
			backoff = epochBackoffMin // a connection that armed was healthy: start again at 1 s
		}
		select {
		case <-t.clock.After(backoff):
		case <-t.ctx.Done():
			return
		}
		if !armedOnce {
			if backoff *= 2; backoff > epochBackoffMax {
				backoff = epochBackoffMax
			}
		}
	}
}

// epochEvent is what the reader goroutine hands over: a classified event.
type epochEvent struct {
	kind string // heartbeat, connected, disposed, other, malformed
}

// connection runs one connection and reports whether the epoch was armed during it.
func (t *EpochTracker) connection() (armedOnce bool) {
	tok, ok := t.newToken()
	if !ok {
		return false // no randomness: never arm, back off
	}
	t.set(tok, false) // a NEW token for every connection, unarmed
	rc, err := t.open(t.ctx)
	if err != nil || rc == nil {
		return false
	}
	events := make(chan epochEvent)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readEpochEvents(rc, events, stop)
	}()
	defer func() {
		close(stop)
		_ = rc.Close()
		<-readerDone
	}()

	last := t.clock.Now()
	gap := t.clock.After(epochGap)
	for {
		select {
		case <-t.ctx.Done():
			return armedOnce
		case ev, open := <-events:
			if !open {
				return armedOnce // the stream ended or errored
			}
			last = t.clock.Now()
			switch ev.kind {
			case "heartbeat":
				t.mu.Lock()
				if !t.armed {
					t.armed, armedOnce = true, true
				}
				t.mu.Unlock()
			case "disposed":
				if nt, ok := t.newToken(); ok {
					t.mu.Lock()
					t.token = nt // replaced; armed stays what it was: the stream is still observed
					t.mu.Unlock()
				} else {
					return armedOnce // cannot make a new token: drop the connection
				}
			case "malformed":
				if nt, ok := t.newToken(); ok {
					t.set(nt, false) // continuity is in doubt: replaced and unarmed until the next heartbeat
				} else {
					return armedOnce
				}
			}
		case <-gap:
			elapsed := t.clock.Now().Sub(last)
			if elapsed >= epochGap {
				return armedOnce // no event for 25 s: the connection is dropped, the epoch replaced
			}
			gap = t.clock.After(epochGap - elapsed)
		}
	}
}

// readEpochEvents parses the SSE stream with bounded memory and sends one classified event per frame. It
// closes the channel when the stream ends or errors, and stops when stop is closed.
func readEpochEvents(r io.Reader, out chan<- epochEvent, stop <-chan struct{}) {
	defer close(out)
	br := bufio.NewReaderSize(r, 4096)
	var data []byte
	oversized, hasData := false, false
	send := func(ev epochEvent) bool {
		select {
		case out <- ev:
			return true
		case <-stop:
			return false
		}
	}
	for {
		line, tooLong, err := readBoundedLine(br, epochMaxLine)
		if err != nil {
			return
		}
		if tooLong {
			oversized = true
		}
		if len(line) == 0 && !tooLong {
			// a blank line ends an event
			if hasData || oversized {
				ev := classifyEpochEvent(data, oversized)
				if !send(ev) {
					return
				}
			}
			data, oversized, hasData = nil, false, false
			continue
		}
		s := string(line)
		switch {
		case strings.HasPrefix(s, ":"): // comment
		case strings.HasPrefix(s, "data:"):
			hasData = true
			v := strings.TrimPrefix(strings.TrimPrefix(s, "data:"), " ")
			if len(data)+len(v)+1 > epochMaxEvent {
				oversized = true
				data = nil
			} else if !oversized {
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, v...)
			}
		}
		// event:, id:, retry: and unknown fields carry nothing the tracker uses
	}
}

// readBoundedLine reads one line up to max bytes; a longer line is consumed and reported tooLong.
func readBoundedLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, false, err
		}
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !isPrefix {
			return buf, tooLong, nil
		}
	}
}

// classifyEpochEvent decodes one frame. An oversized frame is bounded and ignored (a disposal event is
// tiny); a frame that is not a JSON object with a type is "malformed" and invalidates the epoch.
func classifyEpochEvent(data []byte, oversized bool) epochEvent {
	if oversized {
		return epochEvent{kind: "other"}
	}
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &env) != nil {
		return epochEvent{kind: "malformed"}
	}
	typ := env.Type
	if typ == "" && len(env.Payload) > 0 {
		var p struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return epochEvent{kind: "malformed"}
		}
		typ = p.Type
	}
	if typ == "" || len(typ) > epochMaxType {
		return epochEvent{kind: "malformed"}
	}
	switch {
	case typ == "server.heartbeat":
		return epochEvent{kind: "heartbeat"}
	case typ == "server.connected":
		return epochEvent{kind: "connected"}
	case strings.Contains(strings.ToLower(typ), "dispos"):
		return epochEvent{kind: "disposed"}
	}
	return epochEvent{kind: "other"}
}

// GlobalEventOpener returns the opener of GET /global/event for this client: authenticated like every other
// call, no timeout on the stream (a long-lived connection), and a refusal unless the answer is a 200
// text/event-stream (a 401, a redirect or an HTML page never arms).
func (c *Client) GlobalEventOpener() EpochOpener {
	if c == nil {
		return nil
	}
	hc := &http.Client{Transport: c.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context) (io.ReadCloser, error) {
		u := *c.base
		u.Path = "/global/event"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("global event request")
		}
		req.Header.Set("Accept", "text/event-stream")
		if c.password != "" {
			req.SetBasicAuth(c.username, c.password)
		}
		res, err := hc.Do(req)
		if err != nil {
			return nil, errors.New("global event stream unavailable")
		}
		ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if res.StatusCode != http.StatusOK || ct != "text/event-stream" {
			_ = res.Body.Close()
			return nil, &HTTPError{Status: res.StatusCode}
		}
		return res.Body, nil
	}
}
