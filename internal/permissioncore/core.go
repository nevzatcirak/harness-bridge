package permissioncore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sync"
	"time"
	"unicode/utf8"
)

type requestKey struct{ authority, lineage, id string }
type entry struct {
	challenge Challenge
	state     State
	native    NativeStatus
	decision  Decision
	attempted bool
	reason    Error
}
type claim struct {
	owner *Core
	entry *entry
	used  bool
}

// Core owns one bounded in-memory decision table. It is not an execution manager,
// durable audit store, native lock or authentication mechanism. Do not copy it.
type Core struct {
	mu        sync.Mutex
	authority Authority
	config    Config
	epoch     string
	entries   map[requestKey]*entry
	receipts  map[string]*entry
	clock     func() time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	native    nativeProvider // nil in every publicly constructible instance
}

// New always disables native dispatch. There is no public enable flag, setter,
// native adapter, restored decision input or human-channel implementation.
func New(cfg Config, authority Authority) (*Core, error) {
	if cfg.DecisionTTL == 0 {
		cfg.DecisionTTL = 120 * time.Second
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = 256
	}
	if cfg.DecisionTTL < time.Millisecond || cfg.DecisionTTL > 120*time.Second || cfg.Capacity < 1 || cfg.Capacity > 4096 {
		return nil, ErrInput
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, ErrAuthority
	}
	started := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	return &Core{authority: authority, config: cfg, epoch: hex.EncodeToString(epoch[:]), entries: make(map[requestKey]*entry), receipts: make(map[string]*entry), clock: func() time.Duration { return time.Since(started) }, ctx: ctx, cancel: cancel}, nil
}

func opaque(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, b := range []byte(s) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.' || b == ':') {
			return false
		}
	}
	return true
}
func bindingOK(b Binding) bool {
	for _, s := range []string{b.Authority, b.Lineage, b.ProjectRoot, b.Workspace, b.RequestID, b.SessionID, b.ExecutionID, b.UserMessageID, b.ToolMessageID, b.ToolCallID} {
		if !opaque(s, 256) {
			return false
		}
	}
	return opaque(string(b.Principal), 64) && b.NativeVersion == "opencode-1.19.4" && b.Generation > 0 && b.PolicyRevision > 0 && b.ResourceCount <= 32 &&
		(b.Tool == "read" && b.Action == "read" || b.Tool == "edit" && b.Action == "write" || b.Tool == "write" && b.Action == "write" || b.Tool == "bash" && b.Action == "execute")
}
func requestBinding(r Request) (Binding, error) {
	if !bindingOK(r.Binding) || r.Binding.ScopeFingerprint != ([32]byte{}) || len(r.ScopeMaterial) == 0 || len(r.ScopeMaterial) > 16384 || !utf8.Valid(r.ScopeMaterial) {
		return Binding{}, ErrInput
	}
	b := r.Binding
	h := sha256.New()
	h.Write([]byte("HB-PERM-SCOPE-v1\x00"))
	h.Write(r.ScopeMaterial)
	copy(b.ScopeFingerprint[:], h.Sum(nil))
	return b, nil
}
func key(b Binding) requestKey { return requestKey{b.Authority, b.Lineage, b.RequestID} }
func (c *Core) authorize(ctx context.Context, p Principal, b Binding) error {
	if !bindingOK(b) || p != b.Principal || c.authority == nil {
		return ErrAuthority
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.authority.Authorize(ctx, p, b) != nil {
		return ErrAuthority
	}
	return nil
}
func view(e *entry) View {
	return View{Challenge: e.challenge, State: e.state, Choice: e.decision.Choice, Native: e.native, Attempted: e.attempted, Reason: e.reason, Presentation: "Scope details withheld; exact scope requires the independent human channel.", RejectScope: "same_native_session"}
}
func (c *Core) current(e *entry) error {
	if c.closed {
		return ErrClosed
	}
	if e.state == Pending || e.state == Decided || e.state == Reserved {
		now := c.clock()
		if now < 0 || now >= e.challenge.Deadline {
			e.state, e.reason = Expired, ErrExpired
		}
	}
	return nil
}
func (c *Core) lookup(ch Challenge) (*entry, error) {
	if ch.Epoch != c.epoch {
		return nil, ErrBinding
	}
	e := c.entries[key(ch.Binding)]
	if e == nil {
		return nil, ErrNotFound
	}
	if ch != e.challenge {
		return nil, ErrBinding
	}
	if err := c.current(e); err != nil {
		return nil, err
	}
	return e, nil
}
func stateError(e *entry) error {
	switch e.state {
	case Expired:
		return ErrExpired
	case Canceled:
		return ErrCanceled
	case ExternallyResolved:
		return ErrResolved
	case Stale:
		return ErrStale
	case Uncertain:
		return ErrUncertain
	case Denied:
		return ErrUnsafeDeny
	default:
		return ErrUsed
	}
}

// Observe accepts an authoritative request with already-proven execution/tool
// correlation. Reobservation cannot reset expiry, replace bindings or erase spent
// state. A changed request under the same native identity is permanently stale.
func (c *Core) Observe(ctx context.Context, p Principal, r Request) (View, error) {
	b, err := requestBinding(r)
	if err != nil {
		return View{}, err
	}
	if err = c.authorize(ctx, p, b); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return View{}, ErrClosed
	}
	if e := c.entries[key(b)]; e != nil {
		if b != e.challenge.Binding {
			e.state, e.reason = Stale, ErrStale
			if e.attempted {
				e.state = Uncertain
			}
			return View{}, ErrStale
		}
		_ = c.current(e)
		return view(e), nil
	}
	if len(c.entries) >= c.config.Capacity {
		return View{}, ErrCapacity
	}
	now := c.clock()
	if now < 0 || now > time.Duration(math.MaxInt64)-c.config.DecisionTTL {
		return View{}, ErrExpired
	}
	e := &entry{challenge: Challenge{c.epoch, uint64(len(c.entries) + 1), b, now + c.config.DecisionTTL}, state: Pending, native: NativePending}
	c.entries[key(b)] = e
	return view(e), nil
}

func (c *Core) Snapshot(ctx context.Context, p Principal, ch Challenge) (View, error) {
	if err := c.authorize(ctx, p, ch.Binding); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.lookup(ch)
	if err != nil {
		return View{}, err
	}
	return view(e), nil
}

func (c *Core) verifyHuman(ctx context.Context, d Decision) error {
	e, err := c.authority.VerifyHuman(ctx, d.ReceiptID)
	if err != nil || e.Decision != d || !opaque(e.HumanSubject, 128) || e.Channel != IndependentHumanChannel || !e.ExplicitConfirmation || !e.CompletePresentation {
		return ErrHuman
	}
	return nil
}

func (c *Core) Decide(ctx context.Context, p Principal, d Decision) (View, error) {
	if (d.Choice != AllowOnce && d.Choice != Deny) || !opaque(d.ReceiptID, 128) {
		return View{}, ErrInput
	}
	if err := c.authorize(ctx, p, d.Challenge.Binding); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	e, err := c.lookup(d.Challenge)
	c.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	if err = c.verifyHuman(ctx, d); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err = c.current(e); err != nil {
		return View{}, err
	}
	if err = ctx.Err(); err != nil {
		return View{}, err
	}
	if e.decision.ReceiptID != "" {
		if e.decision == d {
			return view(e), nil // disposition only; never a new claim or TTL
		}
		return View{}, ErrConflict
	}
	if e.state != Pending {
		return View{}, stateError(e)
	}
	if other := c.receipts[d.ReceiptID]; other != nil && other != e {
		return View{}, ErrUsed
	}
	e.decision, e.state = d, Decided
	c.receipts[d.ReceiptID] = e
	if d.Choice == Deny {
		// Even one observed pending request is not proof of a safe reject: a new
		// same-session request can race the list. Never dispatch native reject.
		e.state, e.reason = Denied, ErrUnsafeDeny
	}
	return view(e), nil
}

func (c *Core) Claim(ctx context.Context, p Principal, ch Challenge) (Claim, error) {
	if err := c.authorize(ctx, p, ch.Binding); err != nil {
		return Claim{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.lookup(ch)
	if err != nil {
		return Claim{}, err
	}
	if e.state != Decided || e.decision.Choice != AllowOnce {
		return Claim{}, stateError(e)
	}
	if err = ctx.Err(); err != nil {
		return Claim{}, err
	}
	e.state = Reserved
	return Claim{&claim{owner: c, entry: e}}, nil
}

// Reconcile consumes trusted positive native evidence, not a native query. A
// missing request is Unknown. No observation can restore a spent claim/decision.
func (c *Core) Reconcile(ctx context.Context, p Principal, ch Challenge, n NativeEvidence) (View, error) {
	if err := c.authorize(ctx, p, ch.Binding); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.lookup(ch)
	if err != nil {
		return View{}, err
	}
	return c.reconcile(e, n)
}
func (c *Core) reconcile(e *entry, n NativeEvidence) (View, error) {
	if n.Status == NativeUnknown {
		e.native = NativeUnknown
		if e.state == Pending || e.state == Decided || e.state == Reserved || e.attempted {
			e.state, e.reason = Uncertain, ErrUncertain
		}
		return view(e), nil // missing native subject requires no fabricated identity
	}
	if n.Status != NativeResolved && n.Status != NativePending {
		return view(e), ErrInput
	}
	b, err := requestBinding(n.Request)
	if err != nil || b != e.challenge.Binding {
		e.state, e.native, e.reason = Stale, NativeUnknown, ErrStale
		if e.attempted {
			e.state = Uncertain
		}
		return view(e), ErrStale
	}
	switch n.Status {
	case NativeResolved:
		e.native = NativeResolved
		if e.state == Pending || e.state == Decided || e.state == Reserved {
			e.state, e.reason = ExternallyResolved, ErrResolved
		}
	case NativePending:
		e.native = NativePending
	}
	return view(e), nil
}

// Consume spends a sealed claim once. Native execution is disabled in New; the
// private provider exercises only fixture IO. This is NOT Mutation Gate M or a
// durable attempt witness; future integration must add the approved audit/gate
// boundary before enabling any native reply through the sole server authority.
func (c *Core) Consume(ctx context.Context, p Principal, token Claim) (View, error) {
	c.mu.Lock()
	x := token.data
	if x == nil || x.owner != c || x.used {
		c.mu.Unlock()
		return View{}, ErrUsed
	}
	e := x.entry
	if p != e.challenge.Binding.Principal {
		c.mu.Unlock()
		return View{}, ErrAuthority
	}
	if err := c.current(e); err != nil {
		c.mu.Unlock()
		return View{}, err
	}
	if e.state != Reserved {
		err := stateError(e)
		c.mu.Unlock()
		return View{}, err
	}
	x.used = true // claim consumption, before any verification IO; never refunded
	remaining := e.challenge.Deadline - c.clock()
	d := e.decision
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, remaining)
	stop := context.AfterFunc(c.ctx, cancel)
	defer cancel()
	defer stop()
	// An abandoned/panicking fixture call cannot leave an attempted right reusable.
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if e.state == Reserved || e.state == Attempted {
			if e.attempted {
				e.state, e.reason = Uncertain, ErrUncertain
			} else {
				e.state, e.reason = Canceled, ErrCanceled
			}
		}
	}()
	fail := func(reason Error) (View, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if e.state == Reserved {
			e.state, e.reason = Canceled, reason
		}
		return view(e), reason
	}
	if ctx.Err() != nil {
		return fail(ErrCanceled)
	}
	if err := c.authorize(ctx, p, e.challenge.Binding); err != nil {
		return fail(ErrAuthority)
	}
	if err := c.verifyHuman(ctx, d); err != nil {
		return fail(ErrHuman)
	}
	if c.native == nil {
		return fail(ErrDisabled)
	}
	n, err := c.native.Check(ctx, e.challenge.Binding)
	// Policy/human receipt may have changed during the native read. The future
	// integrated gate must additionally linearize its actual policy revisions.
	if err == nil {
		if ctx.Err() != nil {
			return fail(ErrCanceled)
		}
		if c.authorize(ctx, p, e.challenge.Binding) != nil {
			return fail(ErrAuthority)
		}
		if c.verifyHuman(ctx, d) != nil {
			return fail(ErrHuman)
		}
	}
	c.mu.Lock()
	if c.closed || e.state != Reserved || ctx.Err() != nil {
		if e.state == Reserved {
			e.state, e.reason = Canceled, ErrCanceled
		}
		v := view(e)
		c.mu.Unlock()
		return v, ErrCanceled
	}
	_ = c.current(e)
	if e.state != Reserved {
		v := view(e)
		err := stateError(e)
		c.mu.Unlock()
		return v, err
	}
	if err != nil {
		e.state, e.native, e.reason = Uncertain, NativeUnknown, ErrUncertain
		v := view(e)
		c.mu.Unlock()
		return v, ErrUncertain
	}
	v, err := c.reconcile(e, n)
	if err != nil || e.state != Reserved || e.native != NativePending {
		if err == nil {
			err = stateError(e)
		}
		c.mu.Unlock()
		return v, err
	}
	e.state, e.attempted, e.native = Attempted, true, NativeUnknown
	c.mu.Unlock()
	err = c.native.ReplyOnce(ctx, e.challenge.Binding)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil || ctx.Err() != nil || c.closed || e.state != Attempted {
		e.state, e.reason = Uncertain, ErrUncertain
		return view(e), ErrUncertain
	}
	e.state = ReplyObserved // ACK only, not resolution or execution success
	return view(e), nil
}

func (c *Core) Cancel(ctx context.Context, p Principal, ch Challenge) (View, error) {
	if err := c.authorize(ctx, p, ch.Binding); err != nil {
		return View{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, err := c.lookup(ch)
	if err != nil {
		return View{}, err
	}
	if e.state == Attempted {
		e.state, e.reason = Uncertain, ErrUncertain
	} else if e.state == Pending || e.state == Decided || e.state == Reserved {
		e.state, e.reason = Canceled, ErrCanceled
	}
	return view(e), nil
}

// Close fences in-flight work; it does not cancel native work or refund consent.
// Context-ignoring IO cannot be forcibly stopped or given a bounded join here.
func (c *Core) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.cancel()
	for _, e := range c.entries {
		if e.state == Attempted {
			e.state, e.reason = Uncertain, ErrUncertain
		} else if e.state == Pending || e.state == Decided || e.state == Reserved {
			e.state, e.reason = Canceled, ErrClosed
		}
	}
}
