// Package overlay is the persisted Bridge archive overlay of ADR-025 (slice S6).
//
// The overlay is a LABEL store: a bounded set of session IDs the Bridge hides from its own
// catalog and dashboard and refuses prompts to. It never grants, restores or widens anything
// (ADR-025 section 5); nothing in internal/security or internal/lifecycle imports this package,
// and no authorization function consults it.
//
// Every file operation goes through an os.Root opened on the validated directory (openat,
// renameat, unlinkat); the path-based os.Rename, os.Remove, os.OpenFile, os.Create and
// os.WriteFile are never used for the overlay files (an AST test pins it).
package overlay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

const (
	// FileName and LockName are the two files of the overlay directory (ADR-025 4.1).
	FileName  = "overlay.json"
	LockName  = "overlay.lock"
	tmpPrefix = FileName + ".tmp-"

	// MaxEntries and MaxFileBytes are the bounds of ADR-025 4.2 (Q10). The entry count binds first.
	MaxEntries   = 1000
	MaxFileBytes = 512 << 10

	formatVersion = 1
)

// fileBound is MaxFileBytes; a variable only so a test can reach the size bound.
var fileBound int64 = MaxFileBytes

// syncFile is the fsync seam: production calls File.Sync; a test counts the calls to observe that
// the file and the directory are both synced (ADR-025 test 1, mutation "skip the fsync calls").
var syncFile = func(f *os.File) error { return f.Sync() }

// WriteBound is the one deadline of a change (ADR-025 4.3). A variable only so a test can shorten it.
var WriteBound = 5 * time.Second

// Kind classifies an error for the caller's closed error mapping (ADR-025 4.3, 4.4, Q13).
type Kind int

const (
	// KindUnavailable: the overlay state is unknown or the write failed cleanly
	// (UPSTREAM_FAILURE, reason overlay_unavailable).
	KindUnavailable Kind = iota + 1
	// KindUncertain: the write may have happened (UNKNOWN_OUTCOME, reason overlay_uncertain).
	KindUncertain
	// KindTimeout: a change waited its own deadline behind a hung write (UPSTREAM_TIMEOUT, retryable).
	KindTimeout
	// KindLimit: a bound would be exceeded (SESSION_LIMIT).
	KindLimit
	// KindInvalidID: not an archivable ID (never an entry: claudecode:, malformed).
	KindInvalidID
	// KindReadOnly: this process holds no overlay write role.
	KindReadOnly
)

// Error is the typed error of the store.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}
func (e *Error) Unwrap() error { return e.Err }

func fail(k Kind, msg string, err error) error { return &Error{Kind: k, Msg: msg, Err: err} }

// KindOf returns the kind of an overlay error, or 0.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// Entry is one archived session. It is a label; no code reads any field as a permission.
type Entry struct {
	SessionID  string
	ArchivedAt time.Time
	Principal  string
}

type snapshot struct {
	generation uint64
	entries    map[string]Entry
}

// Options of Open.
type Options struct {
	// Writer asks for the write role: the exclusive overlay.lock is taken (non-blocking) and an
	// invalid file is a startup error (Q3). Without it the store only reads and never writes.
	Writer bool
	// Now is the clock of archived_at (the Bridge's own clock). Nil means time.Now.
	Now func() time.Time
}

// Store is the overlay.
type Store struct {
	dir  string
	root *os.Root
	info os.FileInfo // the directory identity recorded at open
	now  func() time.Time

	snap atomic.Pointer[snapshot] // the published snapshot; readers never lock
	bad  atomic.Pointer[error]    // non-nil: the overlay state is unknown

	// mu is overlayMu (ADR-025 4.3): held across read-snapshot, build, write, rename, fsync and publish.
	mu       sync.Mutex
	lockFile *os.File
	canWrite bool

	stateMu       sync.Mutex // guards the uncertain record below; never held across I/O
	uncertain     bool
	pendingGen    uint64
	previousGen   uint64
	lastStat      statKey
	lastStatCheck time.Time
	reader        bool

	// Hook is a fault-injection and delay seam, called at each step of a write with the step name
	// ("open_tmp", "write", "fsync_file", "rename", "fsync_dir", "before_publish", "after_publish").
	// A non-nil error fails that step; a hook may also block. Nil in production; set before use.
	Hook func(step string) error
}

type statKey struct {
	exists bool
	ino    uint64
	size   int64
	mtime  int64
}

// Open validates the overlay directory, takes the write lock when asked, clears leftover
// temporary files (lock holder only) and loads the file once into the snapshot.
func Open(dir string, opts Options) (*Store, error) {
	s := &Store{dir: dir, now: opts.Now, reader: !opts.Writer}
	if s.now == nil {
		s.now = time.Now
	}
	if err := s.openDir(); err != nil {
		return nil, err
	}
	if opts.Writer {
		f, err := s.root.OpenFile(LockName, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			s.root.Close()
			return nil, fmt.Errorf("overlay lock: %w", err)
		}
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || nlink(st) != 1 {
			f.Close()
			s.root.Close()
			return nil, errors.New("overlay lock: not a private regular single-link file")
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			s.lockFile, s.canWrite = f, true
			s.removeLeftovers()
		} else {
			f.Close() // a second process runs without the archive tools (ADR-025 4.3)
		}
	}
	s.lastStatCheck = s.now() // the file was just read: the first poll is a second away (Q8)
	snap, err := s.readFile()
	if err != nil {
		if opts.Writer && s.canWrite {
			s.Close()
			return nil, fmt.Errorf("overlay file invalid (the writer refuses to start): %w", err)
		}
		s.setBad(err)
		s.snap.Store(&snapshot{entries: map[string]Entry{}}) // never consulted while bad
		return s, nil
	}
	s.snap.Store(snap)
	return s, nil
}

func (s *Store) openDir() error {
	if !strings.HasPrefix(s.dir, "/") || strings.Contains(s.dir, "\x00") {
		return errors.New("overlay directory must be an absolute path")
	}
	li, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("overlay directory: %w", err)
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.IsDir() {
		return errors.New("overlay directory must be a real directory (no symlink in the final component)")
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return fmt.Errorf("overlay directory: %w", err)
	}
	st, err := root.Stat(".")
	if err != nil || !os.SameFile(st, li) {
		root.Close()
		return errors.New("overlay directory changed while opening")
	}
	if st.Mode().Perm() != 0o700 || !ownedByUs(st) {
		root.Close()
		return errors.New("overlay directory must be owned by the service user with mode 0700")
	}
	s.root, s.info = root, st
	return nil
}

func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

func nlink(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}

// Close releases the lock and the directory handle.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockFile != nil {
		s.lockFile.Close()
		s.lockFile = nil
	}
	if s.root != nil {
		s.root.Close()
		s.root = nil
	}
}

// CanWrite reports whether this process holds the overlay write role.
func (s *Store) CanWrite() bool { return s.canWrite }

// Err returns the error that made the overlay state unknown, or nil.
func (s *Store) Err() error {
	if p := s.bad.Load(); p != nil {
		return *p
	}
	return nil
}

func (s *Store) setBad(err error) { s.bad.Store(&err) }

// Generation and Entries describe the published snapshot (the closed audit counters).
func (s *Store) Generation() uint64 { return s.snap.Load().generation }

// Entries returns the number of entries in the published snapshot.
func (s *Store) Entries() int { return len(s.snap.Load().entries) }

// Get returns one entry of the published snapshot.
func (s *Store) Get(id string) (Entry, bool) {
	e, ok := s.snap.Load().entries[id]
	return e, ok
}

// Archived answers from the published snapshot with no file I/O in a writer process (ADR-025
// 4.4). An unknown state is an error, never "not archived". A read-only process re-checks the
// file's identity at most once a second (Q8).
func (s *Store) Archived(ctx context.Context, id string) (bool, error) {
	if s.reader {
		s.refresh()
	}
	if err := s.Err(); err != nil {
		return false, fail(KindUnavailable, "archive overlay unavailable", err)
	}
	_, ok := s.snap.Load().entries[id]
	return ok, nil
}

// ArchivedKnown is the memory-only lookup the prompt path uses (ADR-025 4.5, Q1): it keeps the
// last good snapshot when the state is unknown, so an unknown overlay never blocks unrelated
// prompts and a corrupt file after startup cannot affect them.
func (s *Store) ArchivedKnown(id string) bool {
	_, ok := s.snap.Load().entries[id]
	return ok
}

// ---- reading -------------------------------------------------------------------------------

type canonEntry struct {
	SessionID  string `json:"session_id"`
	ArchivedAt string `json:"archived_at"`
	Principal  string `json:"principal"`
}

type canonBody struct {
	V          int          `json:"v"`
	Generation uint64       `json:"generation"`
	Entries    []canonEntry `json:"entries"`
}

type fileBody struct {
	V          *int          `json:"v"`
	Generation *uint64       `json:"generation"`
	Entries    *[]canonEntry `json:"entries"`
	Sum        *string       `json:"sum"`
}

type fileOut struct {
	canonBody
	Sum string `json:"sum"`
}

var principalShape = regexp.MustCompile(`^[A-Za-z0-9._:@/+=-]{1,64}$`)

const timeLayout = "2006-01-02T15:04:05Z"

func encodeCanon(b canonBody) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(b)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func checksum(b canonBody) string {
	sum := sha256.Sum256(encodeCanon(b))
	return hex.EncodeToString(sum[:])
}

// ValidID reports whether id is an archivable session ID: opencode:ses_... only.
func ValidID(id string) bool {
	rest, ok := strings.CutPrefix(id, "opencode:")
	return ok && opencode.ValidID(rest, "ses")
}

// readFile reads and verifies the overlay file. An absent file is the empty overlay; anything
// else invalid is an error (the unknown state).
func (s *Store) readFile() (*snapshot, error) {
	li, err := s.root.Lstat(FileName)
	if errors.Is(err, os.ErrNotExist) {
		s.recordStat(statKey{})
		return &snapshot{entries: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.Mode().IsRegular() {
		return nil, errors.New("overlay file is not a regular file")
	}
	if li.Mode().Perm() != 0o600 || !ownedByUs(li) || nlink(li) != 1 {
		return nil, errors.New("overlay file must be owned by the service user, mode 0600, single link")
	}
	if li.Size() > fileBound {
		return nil, errors.New("overlay file is over the size bound")
	}
	f, err := s.root.Open(FileName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(fi, li) {
		return nil, errors.New("overlay file replaced while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, fileBound+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > fileBound {
		return nil, errors.New("overlay file is over the size bound")
	}
	s.recordStat(keyOf(fi))
	return decode(data)
}

func keyOf(fi os.FileInfo) statKey {
	k := statKey{exists: true, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		k.ino = uint64(st.Ino)
	}
	return k
}

func (s *Store) recordStat(k statKey) {
	s.stateMu.Lock()
	s.lastStat = k
	s.stateMu.Unlock()
}

func decode(data []byte) (*snapshot, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f fileBody
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("overlay file is not valid strict JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("overlay file has trailing data")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("overlay file has trailing data")
	}
	if f.V == nil || f.Generation == nil || f.Entries == nil || f.Sum == nil {
		return nil, errors.New("overlay file is missing a required key")
	}
	if *f.V != formatVersion {
		return nil, errors.New("overlay file has an unsupported version")
	}
	if len(*f.Entries) > MaxEntries {
		return nil, errors.New("overlay file has more than the entry bound")
	}
	snap := &snapshot{generation: *f.Generation, entries: make(map[string]Entry, len(*f.Entries))}
	for _, e := range *f.Entries {
		if !ValidID(e.SessionID) {
			return nil, errors.New("overlay entry is not an opencode: session ID")
		}
		if _, dup := snap.entries[e.SessionID]; dup {
			return nil, errors.New("overlay file has a duplicate entry")
		}
		at, err := time.Parse(timeLayout, e.ArchivedAt)
		if err != nil || at.Format(timeLayout) != e.ArchivedAt {
			return nil, errors.New("overlay entry has a malformed archived_at")
		}
		if !principalShape.MatchString(e.Principal) {
			return nil, errors.New("overlay entry has a malformed principal")
		}
		snap.entries[e.SessionID] = Entry{SessionID: e.SessionID, ArchivedAt: at, Principal: e.Principal}
	}
	if checksum(snap.body()) != *f.Sum {
		return nil, errors.New("overlay checksum mismatch")
	}
	return snap, nil
}

func (sn *snapshot) body() canonBody {
	b := canonBody{V: formatVersion, Generation: sn.generation, Entries: make([]canonEntry, 0, len(sn.entries))}
	for _, e := range sn.entries {
		b.Entries = append(b.Entries, canonEntry{SessionID: e.SessionID, ArchivedAt: e.ArchivedAt.UTC().Format(timeLayout), Principal: e.Principal})
	}
	sort.Slice(b.Entries, func(i, j int) bool { return b.Entries[i].SessionID < b.Entries[j].SessionID })
	return b
}

// refresh is the read-only process reload (Q8): stat the file at most once a second and re-read it
// when (inode, size, mtime) changed. A failed reload keeps the last good snapshot and reports unknown.
func (s *Store) refresh() {
	s.stateMu.Lock()
	if s.now().Sub(s.lastStatCheck) < time.Second && !s.lastStatCheck.IsZero() {
		s.stateMu.Unlock()
		return
	}
	s.lastStatCheck = s.now()
	last := s.lastStat
	s.stateMu.Unlock()
	if s.root == nil {
		return
	}
	var cur statKey
	if fi, err := s.root.Lstat(FileName); err == nil {
		cur = keyOf(fi)
	}
	if cur == last && s.Err() == nil {
		return
	}
	snap, err := s.readFile()
	if err != nil {
		s.setBad(err)
		return
	}
	s.snap.Store(snap)
	s.bad.Store(nil)
}

func (s *Store) removeLeftovers() {
	d, err := s.root.Open(".")
	if err != nil {
		return
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return
	}
	for _, n := range names {
		if leftoverName.MatchString(n) {
			_ = s.root.Remove(n)
		}
	}
}

var leftoverName = regexp.MustCompile(`^overlay\.json\.tmp-[0-9a-f]{16}$`)

// ---- writing -------------------------------------------------------------------------------

// Archive adds one entry. archived is false for an idempotent no-op (the existing entry is
// returned and nothing is written).
func (s *Store) Archive(ctx context.Context, id, principal string) (Entry, bool, error) {
	if !ValidID(id) {
		return Entry{}, false, fail(KindInvalidID, "not an archivable session ID", nil)
	}
	if !principalShape.MatchString(principal) {
		return Entry{}, false, fail(KindInvalidID, "malformed principal", nil)
	}
	type res struct {
		e        Entry
		archived bool
	}
	// The build function runs in the worker, which can outlive the caller after a deadline: it
	// must write no caller variable; its answer travels only through the worker's result.
	out, err := s.change(ctx, func(cur *snapshot) (*snapshot, any, error) {
		if old, ok := cur.entries[id]; ok {
			return nil, res{e: old}, nil
		}
		if len(cur.entries) >= MaxEntries {
			return nil, nil, fail(KindLimit, "the overlay is full", nil)
		}
		e := Entry{SessionID: id, ArchivedAt: s.now().UTC().Truncate(time.Second), Principal: principal}
		next := &snapshot{generation: cur.generation + 1, entries: make(map[string]Entry, len(cur.entries)+1)}
		for k, v := range cur.entries {
			next.entries[k] = v
		}
		next.entries[id] = e
		return next, res{e: e, archived: true}, nil
	})
	if err != nil {
		return Entry{}, false, err
	}
	r := out.(res)
	return r.e, r.archived, nil
}

// Unarchive removes one entry. removed is false for an idempotent no-op.
func (s *Store) Unarchive(ctx context.Context, id string) (bool, error) {
	if !ValidID(id) {
		return false, fail(KindInvalidID, "not an archivable session ID", nil)
	}
	out, err := s.change(ctx, func(cur *snapshot) (*snapshot, any, error) {
		if _, ok := cur.entries[id]; !ok {
			return nil, false, nil
		}
		next := &snapshot{generation: cur.generation + 1, entries: make(map[string]Entry, len(cur.entries))}
		for k, v := range cur.entries {
			if k != id {
				next.entries[k] = v
			}
		}
		return next, true, nil
	})
	if err != nil {
		return false, err
	}
	return out.(bool), nil
}

const (
	opPending int32 = iota
	opAbandoned
	opCommitting
	opCommitted
	opUncertain
)

type result struct {
	out any
	err error
}

func (s *Store) hook(step string) error {
	if s.Hook != nil {
		return s.Hook(step)
	}
	return nil
}

// change runs one serialized change (ADR-025 4.3). Everything after the mutex is taken runs in a
// worker that keeps overlayMu until it returns, so no second write can race a hung one; the
// caller waits only until the deadline.
func (s *Store) change(ctx context.Context, build func(cur *snapshot) (*snapshot, any, error)) (any, error) {
	if !s.canWrite {
		return nil, fail(KindReadOnly, "this process holds no overlay write role", nil)
	}
	timer := time.NewTimer(WriteBound)
	defer timer.Stop()
	locked := make(chan struct{})
	go func() { s.mu.Lock(); close(locked) }()
	select {
	case <-locked:
	case <-timer.C:
		// Hand the late lock back as soon as it is granted: this call is already refused.
		go func() { <-locked; s.mu.Unlock() }()
		return nil, fail(KindTimeout, "a previous overlay write has not returned", nil)
	case <-ctx.Done():
		go func() { <-locked; s.mu.Unlock() }()
		return nil, fail(KindUnavailable, "overlay change canceled", ctx.Err())
	}
	var state atomic.Int32
	done := make(chan result, 1)
	go func() {
		defer s.mu.Unlock()
		out, err := s.commit(&state, build)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-timer.C:
	case <-ctx.Done():
	}
	// The deadline (or cancel) fired with the worker still running.
	if state.CompareAndSwap(opPending, opAbandoned) {
		return nil, fail(KindUnavailable, "the overlay write did not start in time; nothing changed", nil)
	}
	if state.CompareAndSwap(opCommitting, opUncertain) {
		return nil, fail(KindUncertain, "the overlay write is in flight; its outcome is uncertain", nil)
	}
	// The worker finished between the deadline and the check: its own answer stands.
	r := <-done
	return r.out, r.err
}

// commit is the worker body: adopt after an uncertain write, build, write, rename, fsync, publish.
func (s *Store) commit(state *atomic.Int32, build func(cur *snapshot) (*snapshot, any, error)) (any, error) {
	if err := s.adoptIfUncertain(); err != nil {
		return nil, err
	}
	if err := s.Err(); err != nil {
		return nil, fail(KindUnavailable, "archive overlay unavailable", err)
	}
	cur := s.snap.Load() // read after overlayMu is held
	next, res, err := build(cur)
	if err != nil || next == nil {
		return res, err
	}
	out := fileOut{canonBody: next.body()}
	out.Sum = checksum(out.canonBody)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, fail(KindUnavailable, "encode", err)
	}
	if int64(buf.Len()) > fileBound {
		return nil, fail(KindLimit, "the overlay file would exceed its size bound", nil)
	}
	name, err := s.writeTmp(buf.Bytes())
	if err != nil {
		return nil, err
	}
	// The last point a deadline may refuse the call cleanly: the worker re-checks the
	// abandoned flag before the rename (ADR-025 4.3, review #302 P3-1).
	if !state.CompareAndSwap(opPending, opCommitting) {
		_ = s.root.Remove(name)
		return nil, fail(KindUnavailable, "the overlay write was abandoned before the rename", nil)
	}
	// From here the outcome on disk may differ from memory: record it BEFORE the rename is
	// issued, so a rename that hangs is already an uncertain write (ADR-025 4.3).
	s.markUncertain(cur.generation, next.generation)
	if err := s.hook("rename"); err != nil {
		s.clearUncertain()
		_ = s.root.Remove(name)
		return nil, fail(KindUnavailable, "rename failed; nothing changed", err)
	}
	if err := s.root.Rename(name, FileName); err != nil {
		// The rename failed: the old file stands. Nothing changed.
		s.clearUncertain()
		_ = s.root.Remove(name)
		return nil, fail(KindUnavailable, "rename failed; nothing changed", err)
	}
	if err := s.hook("fsync_dir"); err != nil {
		return nil, fail(KindUncertain, "directory fsync failed after the rename", err)
	}
	if err := s.syncDir(); err != nil {
		return nil, fail(KindUncertain, "directory fsync failed after the rename", err)
	}
	if err := s.hook("before_publish"); err != nil {
		return nil, fail(KindUncertain, "publish", err)
	}
	if !state.CompareAndSwap(opCommitting, opCommitted) {
		// The caller already returned the uncertain answer: do not publish; the next change adopts.
		return nil, fail(KindUncertain, "the caller was told the outcome is uncertain", nil)
	}
	s.snap.Store(next) // publish, then release overlayMu (the deferred unlock)
	s.clearUncertain()
	_ = s.hook("after_publish")
	return res, nil
}

func (s *Store) writeTmp(data []byte) (string, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fail(KindUnavailable, "random", err)
	}
	name := tmpPrefix + hex.EncodeToString(rnd[:])
	if err := s.hook("open_tmp"); err != nil {
		return "", fail(KindUnavailable, "open tmp", err)
	}
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fail(KindUnavailable, "open tmp", err)
	}
	cleanup := func() { f.Close(); _ = s.root.Remove(name) }
	if err := s.hook("write"); err != nil {
		cleanup()
		return "", fail(KindUnavailable, "write", err)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return "", fail(KindUnavailable, "write", err)
	}
	if err := s.hook("fsync_file"); err != nil {
		cleanup()
		return "", fail(KindUnavailable, "fsync", err)
	}
	if err := syncFile(f); err != nil {
		cleanup()
		return "", fail(KindUnavailable, "fsync", err)
	}
	if err := f.Close(); err != nil {
		_ = s.root.Remove(name)
		return "", fail(KindUnavailable, "close", err)
	}
	return name, nil
}

func (s *Store) syncDir() error {
	d, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return syncFile(d)
}

func (s *Store) markUncertain(prev, pending uint64) {
	s.stateMu.Lock()
	s.uncertain, s.previousGen, s.pendingGen = true, prev, pending
	s.stateMu.Unlock()
}

func (s *Store) clearUncertain() {
	s.stateMu.Lock()
	s.uncertain = false
	s.stateMu.Unlock()
}

// adoptIfUncertain is step zero of the next change (ADR-025 4.3): re-read the file under overlayMu
// and the flock; the pending generation is adopted, the previous one just proceeds, anything else
// latches unknown until restart.
func (s *Store) adoptIfUncertain() error {
	s.stateMu.Lock()
	uncertain, pending, previous := s.uncertain, s.pendingGen, s.previousGen
	s.stateMu.Unlock()
	if !uncertain {
		return nil
	}
	snap, err := s.readFile()
	if err != nil {
		s.setBad(err)
		return fail(KindUnavailable, "the overlay file is unreadable after an uncertain write", err)
	}
	switch snap.generation {
	case pending:
		s.snap.Store(snap)
	case previous:
		// the earlier write did not commit: proceed from the current snapshot
	default:
		err := fmt.Errorf("overlay generation %d is neither the pending %d nor the previous %d", snap.generation, pending, previous)
		s.setBad(err)
		return fail(KindUnavailable, "the overlay changed unexpectedly after an uncertain write", err)
	}
	s.clearUncertain()
	return nil
}
