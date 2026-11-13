package claudecode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This is the ADR-021 §5 history record store, minus retention deletion and the
// writer lock (slice 2 wires those). It assumes ONE writer process per
// directory: the per-session writer lock of §2 provides that. Within the process
// a mutex serialises every write, so seq is strictly increasing.

// Record kinds (§5).
const (
	KindTurnStart      = "turn_start"
	KindAssistantBlock = "assistant_block"
	KindToolResult     = "tool_result"
	KindTurnEnd        = "turn_end"
	KindRestartGap     = "restart_gap"
	KindTruncated      = "truncated"
)

// Errors.
var (
	ErrPromptTooLong      = errors.New("claudecode: prompt exceeds 32768 bytes")
	ErrSessionHistoryFull = errors.New("claudecode: session history is at its size bound; archive segments before a new execution")
	ErrHistoryCorrupt     = errors.New("claudecode: history_corrupt")
	ErrNoTurn             = errors.New("claudecode: no open turn for this execution")
	// ErrInvalidTurnEnd: a turn_end field was outside its closed set. The turn_end
	// is still written, with the offending field replaced by a safe value, so a
	// finished turn never reads as open.
	ErrInvalidTurnEnd = errors.New("claudecode: turn_end field outside its closed set")
)

const maxPromptBytes = 32768

// StoreConfig holds the §5 bounds. Zero fields take the ADR defaults.
type StoreConfig struct {
	MaxRecordBytes  int64 // 64 KiB
	MaxTurnBytes    int64 // 2 MiB
	MaxSegmentBytes int64 // 8 MiB
	MaxSessionBytes int64 // 64 MiB
	MaxReadBytes    int64 // 16 MiB: the most Read loads, newest segments first
	Now             func() time.Time
}

// DefaultStoreConfig returns the ADR defaults.
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{MaxRecordBytes: 64 << 10, MaxTurnBytes: 2 << 20, MaxSegmentBytes: 8 << 20, MaxSessionBytes: 64 << 20, MaxReadBytes: 16 << 20, Now: time.Now}
}

func (c StoreConfig) withDefaults() StoreConfig {
	d := DefaultStoreConfig()
	if c.MaxRecordBytes <= 0 {
		c.MaxRecordBytes = d.MaxRecordBytes
	}
	if c.MaxTurnBytes <= 0 {
		c.MaxTurnBytes = d.MaxTurnBytes
	}
	if c.MaxSegmentBytes <= 0 {
		c.MaxSegmentBytes = d.MaxSegmentBytes
	}
	if c.MaxSessionBytes <= 0 {
		c.MaxSessionBytes = d.MaxSessionBytes
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = d.MaxReadBytes
	}
	if c.Now == nil {
		c.Now = d.Now
	}
	return c
}

// Block is the payload of an assistant_block record.
type Block struct {
	Type  string `json:"type"` // text | tool_use
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input string `json:"input,omitempty"`
}

// Record is one NDJSON line: {v, seq, at, execution_id, kind, ...}.
type Record struct {
	V           int       `json:"v"`
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	ExecutionID string    `json:"execution_id"`
	Kind        string    `json:"kind"`

	Prompt        string `json:"prompt,omitempty"`
	Principal     string `json:"principal,omitempty"`
	KeyHashPrefix string `json:"key_hash_prefix,omitempty"`

	Block *Block `json:"block,omitempty"`

	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`

	State          string  `json:"state,omitempty"`
	Row            string  `json:"row,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	Subtype        string  `json:"subtype,omitempty"`
	TerminalReason string  `json:"terminal_reason,omitempty"`
	Denials        int     `json:"permission_denials,omitempty"`
	NumTurns       int     `json:"num_turns,omitempty"`
	ExitClass      string  `json:"exit_class,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`

	OriginalKind string `json:"original_kind,omitempty"`
	OriginalSize int64  `json:"original_size,omitempty"`
}

// TurnStart describes the user prompt as sent. KeyHashPrefix is a hex prefix of
// the idempotency-key hash, never the key.
type TurnStart struct {
	Prompt        string
	Principal     string
	KeyHashPrefix string
}

// TurnEnd carries the terminal decision of §3.
type TurnEnd struct {
	State, Row, Reason, Subtype, TerminalReason, ExitClass string
	NumTurns, Denials                                      int
	CostUSD                                                float64
}

// ReadResult is the readable history of one session. Corrupt is set when reading
// stopped at a record that cannot be trusted; Records holds what precedes it.
type ReadResult struct {
	Records []Record
	Corrupt bool
	// Partial is set when Read stopped at its byte bound and older segments were
	// not loaded: Records is the newest complete suffix of the history.
	Partial bool
}

type turnState struct {
	bytes     int64
	truncated bool
}

type sessionState struct {
	loaded   bool
	nextSeq  int64
	seg      int
	segBytes int64
	total    int64
	turns    map[string]*turnState
}

// Store is the history record store of one directory.
type Store struct {
	dirf     *os.File // the history directory, opened once and checked on the descriptor
	cfg      StoreConfig
	mu       sync.Mutex
	sessions map[string]*sessionState
}

var (
	execIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	keyHashPattern = regexp.MustCompile(`^[0-9a-f]{0,16}$`)
)

// OpenStore validates dir (ADR-021 §5) and returns a Store.
//
// The path is validated once for its ancestors (ValidateHistoryDir), then the
// directory is opened without following a final symlink and checked on the
// descriptor; every later operation is relative to that descriptor.
func OpenStore(dir string, cfg StoreConfig) (*Store, error) {
	if err := ValidateHistoryDir(OSFS{}, dir, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	f, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	return &Store{dirf: f, cfg: cfg.withDefaults(), sessions: map[string]*sessionState{}}, nil
}

// Close releases the directory descriptor. The Store is unusable afterwards.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirf == nil {
		return nil
	}
	err := s.dirf.Close()
	s.dirf = nil
	return err
}

func sessionHash(uuid string) string {
	h := sha256.Sum256([]byte(SessionPrefix + uuid))
	return hex.EncodeToString(h[:])
}

func segmentName(uuid string, n int) string {
	return fmt.Sprintf("%s-%06d.ndjson", sessionHash(uuid), n)
}

// BeginTurn opens a turn and writes its turn_start. It refuses when the session
// is at its total size bound: existing records are never dropped.
func (s *Store) BeginTurn(uuid, exec string, t TurnStart) error {
	if !ValidUUID(uuid) || !execIDPattern.MatchString(exec) || !keyHashPattern.MatchString(t.KeyHashPrefix) {
		return errors.New("claudecode: invalid history identifiers")
	}
	if len(t.Prompt) > maxPromptBytes {
		return ErrPromptTooLong
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.session(uuid)
	if err != nil {
		return err
	}
	if st.total >= s.cfg.MaxSessionBytes {
		return ErrSessionHistoryFull
	}
	st.turns[exec] = &turnState{}
	rec := Record{Kind: KindTurnStart, ExecutionID: exec, Prompt: Redact(t.Prompt), Principal: Redact(boundedASCII(t.Principal, 128)), KeyHashPrefix: t.KeyHashPrefix}
	return s.write(uuid, st, rec, st.turns[exec])
}

// Append writes an assistant_block or tool_result of an open turn. A record over
// the record bound becomes a truncated marker; once a turn is over its budget the
// rest become one truncated record.
func (s *Store) Append(uuid, exec string, r Record) error {
	if !ValidUUID(uuid) || !execIDPattern.MatchString(exec) {
		return errors.New("claudecode: invalid history identifiers")
	}
	if r.Kind != KindAssistantBlock && r.Kind != KindToolResult {
		return errors.New("claudecode: Append takes assistant_block or tool_result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.session(uuid)
	if err != nil {
		return err
	}
	ts := st.turns[exec]
	if ts == nil {
		return ErrNoTurn
	}
	r.ExecutionID = exec
	if r.Block != nil {
		b := *r.Block
		b.Text, b.Input, b.Name = Redact(b.Text), Redact(b.Input), Redact(boundedASCII(b.Name, 128))
		r.Block = &b
	}
	r.Content, r.ToolUseID = Redact(r.Content), boundedASCII(r.ToolUseID, 128)
	return s.write(uuid, st, r, ts)
}

// EndTurn writes the turn_end. It is always written, even past the turn budget,
// so a finished turn never reads as open.
func (s *Store) EndTurn(uuid, exec string, e TurnEnd) error {
	if !ValidUUID(uuid) || !execIDPattern.MatchString(exec) {
		return errors.New("claudecode: invalid history identifiers")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.session(uuid)
	if err != nil {
		return err
	}
	ts := st.turns[exec]
	if ts == nil {
		return ErrNoTurn
	}
	e, invalid := e.closed()
	rec := Record{Kind: KindTurnEnd, ExecutionID: exec, State: e.State, Row: e.Row, Reason: e.Reason, Subtype: e.Subtype,
		TerminalReason: e.TerminalReason, Denials: e.Denials, NumTurns: e.NumTurns, ExitClass: e.ExitClass, CostUSD: e.CostUSD}
	err = s.write(uuid, st, rec, nil)
	delete(st.turns, exec)
	if err == nil && invalid {
		err = ErrInvalidTurnEnd
	}
	return err
}

// write assigns seq/at, applies the bounds and appends one line with one write.
// ts is the turn whose budget applies (nil: exempt).
func (s *Store) write(uuid string, st *sessionState, r Record, ts *turnState) error {
	r.V, r.At = 1, s.cfg.Now().UTC()
	r.Seq = st.nextSeq
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if int64(len(line))+1 > s.cfg.MaxRecordBytes {
		line, err = json.Marshal(Record{V: 1, Seq: r.Seq, At: r.At, ExecutionID: r.ExecutionID, Kind: KindTruncated, OriginalKind: r.Kind, OriginalSize: int64(len(line)) + 1})
		if err != nil {
			return err
		}
	}
	if ts != nil {
		if ts.truncated {
			return nil
		}
		if ts.bytes+int64(len(line))+1 > s.cfg.MaxTurnBytes {
			size := int64(len(line)) + 1
			ts.truncated = true
			line, err = json.Marshal(Record{V: 1, Seq: r.Seq, At: r.At, ExecutionID: r.ExecutionID, Kind: KindTruncated, OriginalKind: r.Kind, OriginalSize: size})
			if err != nil {
				return err
			}
		}
		ts.bytes += int64(len(line)) + 1
	}
	line = append(line, '\n')
	if st.segBytes > 0 && st.segBytes+int64(len(line)) > s.cfg.MaxSegmentBytes {
		st.seg++
		st.segBytes = 0
	}
	if st.seg == 0 {
		st.seg = 1
	}
	if err := appendLine(s.dirf, segmentName(uuid, st.seg), line); err != nil {
		return err
	}
	st.nextSeq++
	st.segBytes += int64(len(line))
	st.total += int64(len(line))
	return nil
}

// session loads the on-disk state of a session once: segment sizes, the next
// seq, and whether the last segment ends in a torn line (then the next record
// goes to a NEW segment, so a torn tail is never extended).
func (s *Store) session(uuid string) (*sessionState, error) {
	if st := s.sessions[uuid]; st != nil && st.loaded {
		return st, nil
	}
	st := &sessionState{nextSeq: 1, turns: map[string]*turnState{}}
	segs, err := s.segments(uuid)
	if err != nil {
		return nil, err
	}
	var last []byte
	for i, n := range segs {
		data, err := s.readSegment(uuid, n)
		if err != nil {
			return nil, err
		}
		st.total += int64(len(data))
		st.seg, st.segBytes = n, int64(len(data))
		if i == len(segs)-1 {
			last = data
		}
	}
	if recs, _ := parseSegment(last); len(recs) > 0 {
		st.nextSeq = recs[len(recs)-1].Seq + 1
	} else if len(segs) > 1 {
		// An empty or unreadable last segment: continue after the previous one.
		if prev, err := s.readSegment(uuid, segs[len(segs)-2]); err == nil {
			if recs, _ := parseSegment(prev); len(recs) > 0 {
				st.nextSeq = recs[len(recs)-1].Seq + 1
			}
		}
	}
	if len(last) > 0 && last[len(last)-1] != '\n' {
		st.seg++
		st.segBytes = 0
	}
	st.loaded = true
	s.sessions[uuid] = st
	return st, nil
}

func (s *Store) segments(uuid string) ([]int, error) {
	if s.dirf == nil {
		return nil, errors.New("claudecode: history store is closed")
	}
	if _, err := s.dirf.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ents, err := s.dirf.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	prefix := sessionHash(uuid) + "-"
	var out []int
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".ndjson"))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("claudecode: unexpected history entry %q", name)
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

func (s *Store) readSegment(uuid string, n int) ([]byte, error) {
	limit := s.cfg.MaxSegmentBytes + s.cfg.MaxRecordBytes + 4096
	return readFile(s.dirf, segmentName(uuid, n), limit)
}

// parseSegment returns the valid records of one segment. An unparsable final
// line without a trailing newline is a torn write and is ignored; an unparsable
// line that ends in a newline is corruption.
func parseSegment(data []byte) ([]Record, bool) {
	var recs []Record
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var ln []byte
		terminated := i >= 0
		if terminated {
			ln, data = data[:i], data[i+1:]
		} else {
			ln, data = data, nil
		}
		var r Record
		if err := json.Unmarshal(ln, &r); err != nil || r.V != 1 || r.Seq < 1 || r.Kind == "" {
			if !terminated && len(data) == 0 {
				return recs, false
			}
			return recs, true
		}
		recs = append(recs, r)
	}
	return recs, false
}

// Read returns the records of a session in order. Corruption (an unparsable
// terminated line, or a seq that is not the successor of the previous one) stops
// the read: ErrHistoryCorrupt is returned with the records that precede it.
func (s *Store) Read(uuid string) (ReadResult, error) {
	if !ValidUUID(uuid) {
		return ReadResult{}, errors.New("claudecode: invalid session uuid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(uuid)
}

func (s *Store) read(uuid string) (ReadResult, error) { return s.readBounded(uuid, s.cfg.MaxReadBytes) }

// readBounded loads segments newest first until adding the next one would pass
// limit (limit <= 0: no bound, used only by the startup recovery, which is itself
// bounded by MaxSessionBytes). The newest segment is always loaded. Older
// segments left out set Partial.
func (s *Store) readBounded(uuid string, limit int64) (ReadResult, error) {
	segs, err := s.segments(uuid)
	if err != nil {
		return ReadResult{}, err
	}
	var res ReadResult
	datas := make([][]byte, 0, len(segs))
	var acc int64
	for i := len(segs) - 1; i >= 0; i-- {
		data, err := s.readSegment(uuid, segs[i])
		if err != nil {
			return res, err
		}
		if limit > 0 && len(datas) > 0 && acc+int64(len(data)) > limit {
			res.Partial = true
			break
		}
		acc += int64(len(data))
		datas = append(datas, data)
	}
	for i := len(datas) - 1; i >= 0; i-- {
		data := datas[i]
		recs, bad := parseSegment(data)
		for _, r := range recs {
			if len(res.Records) > 0 && r.Seq != res.Records[len(res.Records)-1].Seq+1 {
				res.Corrupt = true
				return res, ErrHistoryCorrupt
			}
			res.Records = append(res.Records, r)
		}
		if bad {
			res.Corrupt = true
			return res, ErrHistoryCorrupt
		}
	}
	return res, nil
}

// RecoverOpenTurns appends a restart_gap for every turn that has a turn_start and
// neither a turn_end nor a restart_gap (§5, Restart behavior). It changes no
// execution state. It returns the execution IDs it marked.
func (s *Store) RecoverOpenTurns(uuid string) ([]string, error) {
	if !ValidUUID(uuid) {
		return nil, errors.New("claudecode: invalid session uuid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.readBounded(uuid, 0)
	if err != nil {
		return nil, err
	}
	open := map[string]bool{}
	var order []string
	for _, r := range res.Records {
		switch r.Kind {
		case KindTurnStart:
			if !open[r.ExecutionID] {
				order = append(order, r.ExecutionID)
			}
			open[r.ExecutionID] = true
		case KindTurnEnd, KindRestartGap:
			open[r.ExecutionID] = false
		}
	}
	st, err := s.session(uuid)
	if err != nil {
		return nil, err
	}
	var marked []string
	for _, id := range order {
		if !open[id] {
			continue
		}
		open[id] = false
		if err := s.write(uuid, st, Record{Kind: KindRestartGap, ExecutionID: id}, nil); err != nil {
			return marked, err
		}
		marked = append(marked, id)
	}
	return marked, nil
}

// The closed sets of the turn_end strings (ADR-021 §3, §9). Nothing outside them
// is stored: the values come from the state machine, never from the child.
var (
	turnEndStates  = map[string]bool{"completed": true, "failed": true, "interrupted": true, "unknown": true}
	turnEndRows    = map[string]bool{"0": true, "9": true, "6a": true, "9s": true, "6b": true, "1": true, "1s": true, "2": true, "8": true, "3": true, "4": true, "5": true, "6": true, "7": true, "10": true}
	turnEndReasons = map[string]bool{"": true, "EXIT_UNCONFIRMED": true, "MONITOR_EXPIRED": true, "MANAGER_CLOSED": true, "ABORT_UNCONFIRMED": true,
		"SUBMISSION_UNCONFIRMED": true, "IDENTITY_MISMATCH": true, "PROCESS_ENDED_WITHOUT_RESULT": true}
	turnEndExits = map[string]bool{"": true, "zero": true, "nonzero": true, "signal": true, "unknown": true, "none": true}
	enumString   = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,64}$`)
)

// closed returns e with every out-of-set field replaced by a safe value and
// whether any was replaced. Subtype and TerminalReason are child-reported and
// cannot be a closed set (an unlisted terminal_reason is a verdict input), so
// they must be a short identifier; anything else becomes "other".
func (e TurnEnd) closed() (TurnEnd, bool) {
	invalid := false
	if !turnEndStates[e.State] {
		e.State, invalid = "unknown", true
	}
	if !turnEndRows[e.Row] {
		e.Row, invalid = "10", true
	}
	if !turnEndReasons[e.Reason] {
		e.Reason, invalid = "", true
	}
	if !turnEndExits[e.ExitClass] {
		e.ExitClass, invalid = "unknown", true
	}
	if !enumString.MatchString(e.Subtype) {
		e.Subtype = "other"
	}
	if !enumString.MatchString(e.TerminalReason) {
		e.TerminalReason = "other"
	}
	return e, invalid
}

// APIRecord is the only shape of a history record that leaves the process
// (read_session_history). Each kind exposes a fixed set of fields: the principal,
// the idempotency-key hash prefix, the state-machine row and the format version
// are internal and never serialised here.
type APIRecord struct {
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	ExecutionID string    `json:"execution_id"`
	Kind        string    `json:"kind"`

	Prompt    string `json:"prompt,omitempty"`
	Block     *Block `json:"block,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`

	State          string  `json:"state,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	Subtype        string  `json:"subtype,omitempty"`
	TerminalReason string  `json:"terminal_reason,omitempty"`
	Denials        int     `json:"permission_denials,omitempty"`
	NumTurns       int     `json:"num_turns,omitempty"`
	ExitClass      string  `json:"exit_class,omitempty"`
	CostUSD        float64 `json:"cost_usd_reported,omitempty"`

	OriginalKind string `json:"original_kind,omitempty"`
	OriginalSize int64  `json:"original_size,omitempty"`
}

// API projects a record onto its per-kind whitelist.
func (r Record) API() APIRecord {
	a := APIRecord{Seq: r.Seq, At: r.At, ExecutionID: r.ExecutionID, Kind: r.Kind}
	switch r.Kind {
	case KindTurnStart:
		a.Prompt = r.Prompt
	case KindAssistantBlock:
		a.Block = r.Block
	case KindToolResult:
		a.ToolUseID, a.Content = r.ToolUseID, r.Content
	case KindTurnEnd:
		a.State, a.Reason, a.Subtype, a.TerminalReason = r.State, r.Reason, r.Subtype, r.TerminalReason
		a.Denials, a.NumTurns, a.ExitClass, a.CostUSD = r.Denials, r.NumTurns, r.ExitClass, r.CostUSD
	case KindTruncated:
		a.OriginalKind, a.OriginalSize = r.OriginalKind, r.OriginalSize
	}
	return a
}

// APIResult is what read_session_history serialises.
type APIResult struct {
	Records []APIRecord `json:"records"`
	Corrupt bool        `json:"history_corrupt,omitempty"`
	Partial bool        `json:"history_truncated,omitempty"`
}

// ReadAPI reads the bounded history and projects every record onto the whitelist.
func (s *Store) ReadAPI(uuid string) (APIResult, error) {
	res, err := s.Read(uuid)
	out := APIResult{Records: make([]APIRecord, 0, len(res.Records)), Corrupt: res.Corrupt, Partial: res.Partial}
	for _, r := range res.Records {
		out.Records = append(out.Records, r.API())
	}
	return out, err
}
