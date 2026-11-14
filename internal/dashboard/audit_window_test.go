package dashboard

import (
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Review #252 P2-1: the swap of the process logger must be unobservable to concurrent
// loggers. These tests use the real default handler and the real log package.

// lockedBuf is a concurrency-safe destination for the log package.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// loggingState saves and restores the process logger state of one test.
func loggingState(t *testing.T, w io.Writer, flags int, prefix string) {
	t.Helper()
	pw, pf, pp, pd := log.Writer(), log.Flags(), log.Prefix(), slog.Default()
	log.SetOutput(w)
	log.SetFlags(flags)
	log.SetPrefix(prefix)
	t.Cleanup(func() {
		teeInstallWindow, teeRestoreWindow = nil, nil
		slog.SetDefault(pd)
		log.SetOutput(pw)
		log.SetFlags(pf)
		log.SetPrefix(pp)
	})
}

func within(t *testing.T, what string, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v: the process logger is deadlocked", what, d)
	}
}

// (i) A line logged INSIDE the install window and INSIDE the restore window, by the log
// package and by slog, neither hangs nor is lost. RED on 6ed96d2: the journal branch was the
// default handler, which wrote through the log lock the call already held.
func TestLoggingInsideTheInstallAndRestoreWindowsDoesNotDeadlock(t *testing.T) {
	out := &lockedBuf{}
	loggingState(t, out, 0, "")
	ring := NewAuditRing()
	teeInstallWindow = func() {
		log.Printf("plain line in the install window")
		slog.Info("explicit interrupt", "event", "interrupt", "outcome", "in-install-window")
	}
	teeRestoreWindow = func() {
		log.Printf("plain line in the restore window")
		slog.Info("explicit interrupt", "event", "interrupt", "outcome", "in-restore-window")
	}
	var restore func()
	within(t, "InstallAuditTee", 5*time.Second, func() { restore = InstallAuditTee(ring) })
	within(t, "the restore", 5*time.Second, func() { restore() })
	got := out.String()
	for _, want := range []string{"plain line in the install window", "outcome=in-install-window", "plain line in the restore window", "outcome=in-restore-window"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("%q appears %d times in the journal, want exactly once:\n%s", want, strings.Count(got, want), got)
		}
	}
	// What the ring saw: the slog line of the install window (the tee was already the
	// default); the slog line of the restore window went to the original handler.
	if n := len(ring.Entries()); n != 1 || ring.Entries()[0].Attr("outcome") != "in-install-window" {
		t.Errorf("the ring holds %+v", ring.Entries())
	}
	// A line logged in the install window by the log package carries the level word: it
	// went through the tee as an Info record (documented in InstallAuditTee).
	if !strings.Contains(got, "INFO plain line in the install window") {
		t.Errorf("the install-window log line is not the documented INFO form:\n%s", got)
	}
	// After install and after restore log.Printf still reaches the journal, plainly.
	log.Printf("after restore")
	if !strings.HasSuffix(out.String(), "after restore\n") || strings.Contains(out.String(), "INFO after restore") {
		t.Errorf("a plain log line after restore: %q", out.String())
	}
	if log.Writer() != io.Writer(out) {
		t.Errorf("the log package was not pointed back at the original writer: %T", log.Writer())
	}
	if fmt.Sprintf("%T", slog.Default().Handler()) != "*slog.defaultHandler" {
		t.Errorf("the original default handler was not put back: %T", slog.Default().Handler())
	}
}

func TestPlainLogLinesStillReachTheJournalAfterInstall(t *testing.T) {
	out := &lockedBuf{}
	loggingState(t, out, 0, "")
	restore := InstallAuditTee(NewAuditRing())
	defer restore()
	log.Printf("plain %d", 7)
	if out.String() != "plain 7\n" {
		t.Fatalf("a plain log line after install = %q", out.String())
	}
	slog.Info("remote boundary", "boundary_allowed", true)
	if !strings.HasSuffix(out.String(), "INFO remote boundary boundary_allowed=true\n") {
		t.Fatalf("a slog line after install = %q", out.String())
	}
}

// (ii) Hammer: install and restore, repeatedly, while loggers run on every side. Run under
// -race. RED on 6ed96d2 (deadlock within a few iterations).
func TestInstallRestoreUnderConcurrentLoggers(t *testing.T) {
	out := &lockedBuf{}
	loggingState(t, out, 0, "")
	prev := slog.Default()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if i%2 == 0 {
					log.Printf("plain %d-%d", w, i)
				} else {
					slog.Info("explicit interrupt", "event", "interrupt", "execution_id", fmt.Sprintf("w%d-%d", w, i))
				}
				if i%16 == 0 {
					runtime.Gosched()
				}
			}
		}(w)
	}
	within(t, "200 install/restore cycles under concurrent loggers", 60*time.Second, func() {
		for i := 0; i < 200; i++ {
			restore := InstallAuditTee(NewAuditRing())
			runtime.Gosched()
			restore()
		}
	})
	close(stop)
	within(t, "the loggers to stop", 10*time.Second, wg.Wait)
	if slog.Default() != prev {
		t.Fatal("the original default logger was not put back")
	}
	if out.String() == "" || !strings.Contains(out.String(), "plain ") {
		t.Fatal("no line reached the journal")
	}
	log.Printf("final")
	if !strings.HasSuffix(out.String(), "final\n") {
		t.Fatalf("the log package no longer writes plainly: %q", out.String()[max(0, len(out.String())-40):])
	}
}

// The journal branch of the tee equals the default handler byte for byte over a wide set of
// records: levels, quoting, groups, With, attributes named like the built-in fields, errors,
// durations, multi-line and empty messages, and the log flags and prefix.
func TestStdJournalMatchesTheDefaultHandlerByteForByte(t *testing.T) {
	type rec func(l *slog.Logger)
	cases := map[string]rec{
		"plain":            func(l *slog.Logger) { l.Info("hello") },
		"empty message":    func(l *slog.Logger) { l.Info("", "k", "v") },
		"multi-line":       func(l *slog.Logger) { l.Warn("two\nlines", "a", 1) },
		"trailing newline": func(l *slog.Logger) { l.Info("ends\n", "a", 1) },
		"levels":           func(l *slog.Logger) { l.Error("e"); l.Warn("w"); l.Log(nil, slog.LevelInfo+2, "custom", "k", "v") },
		"quoting": func(l *slog.Logger) {
			l.Info("q", "a b", "c d", "eq", "x=y", "quote", `he said "hi"`, "empty", "", "uni", "é✓", "ctl", "a\tb")
		},
		"kinds": func(l *slog.Logger) {
			l.Info("k", "i", 1, "u", uint64(2), "f", 1.5, "b", true, "d", 1500*time.Millisecond, "e", errors.New("boom"), "any", []int{1, 2}, "nil", nil)
		},
		"time value":    func(l *slog.Logger) { l.Info("t", "at", time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC)) },
		"builtin names": func(l *slog.Logger) { l.Info("b", "time", "T", "level", "L", "msg", "M", "source", "S") },
		"group attr": func(l *slog.Logger) {
			l.Info("g", slog.Group("grp", slog.Int("a", 1), slog.Group("in", slog.String("b", "x"))))
		},
		"empty group":      func(l *slog.Logger) { l.Info("g", slog.Group("empty")) },
		"with":             func(l *slog.Logger) { l.With("pre", 1, "pre2", "two words").Info("w", "post", 2) },
		"with group":       func(l *slog.Logger) { l.WithGroup("g1").With("x", 1).WithGroup("g2").Info("wg", "y", 2) },
		"with then group":  func(l *slog.Logger) { l.With("a", 1).WithGroup("g").Info("m", "b", 2) },
		"empty group name": func(l *slog.Logger) { l.WithGroup("").Info("m", "b", 2) },
		"valuer":           func(l *slog.Logger) { l.Info("v", "lv", slog.AnyValue(valuer{})) },
		"many": func(l *slog.Logger) {
			for i := 0; i < 40; i++ {
				l.Info("many", "i", i, "s", strings.Repeat("x", i))
			}
		},
		"debug is dropped": func(l *slog.Logger) { l.Debug("below the default level", "k", "v") },
		"audit shaped": func(l *slog.Logger) {
			l.Info("prompt submission", "event", "prompt_submission", "tool", "send_prompt", "outcome", "new", "accepted", true, "session_id", "opencode:ses_a", "execution_id", "abc", "idem", "0123456789ab", "principal", "local")
		},
	}
	for _, fl := range []struct {
		name   string
		flags  int
		prefix string
	}{{"flags 0", 0, ""}, {"prefix and msgprefix", log.Lmsgprefix, "[hb] "}, {"prefix without msgprefix", 0, "hb: "}} {
		for name, c := range cases {
			t.Run(fl.name+"/"+name, func(t *testing.T) {
				want := &lockedBuf{}
				loggingState(t, want, fl.flags, fl.prefix)
				c(slog.Default())
				without := want.String()
				got := &lockedBuf{}
				log.SetOutput(got)
				restore := InstallAuditTee(NewAuditRing())
				c(slog.Default())
				restore()
				if got.String() != without {
					t.Fatalf("the journal differs under the tee\nwant %q\ngot  %q", without, got.String())
				}
			})
		}
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.GroupValue(slog.String("a", "1"), slog.Int("b", 2)) }

// The standard flags (date and time) work through the private logger as through the global one.
func TestStdJournalCarriesTheStandardLogHeader(t *testing.T) {
	out := &lockedBuf{}
	loggingState(t, out, log.LstdFlags, "")
	restore := InstallAuditTee(NewAuditRing())
	defer restore()
	slog.Info("explicit interrupt", "event", "interrupt")
	log.Printf("plain")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	re := regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	if len(lines) != 2 || !re.MatchString(lines[0]) || !re.MatchString(lines[1]) || !strings.HasSuffix(lines[0], " INFO explicit interrupt event=interrupt") || !strings.HasSuffix(lines[1], " plain") {
		t.Fatalf("lines: %q", lines)
	}
}

// A program that installed its own default handler keeps it as the journal branch.
func TestACustomDefaultHandlerIsTheJournalBranchAsItIs(t *testing.T) {
	out := &lockedBuf{}
	loggingState(t, io.Discard, 0, "")
	custom := slog.NewTextHandler(out, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})
	slog.SetDefault(slog.New(custom))
	restore := InstallAuditTee(NewAuditRing())
	defer restore()
	slog.Info("explicit interrupt", "event", "interrupt")
	if !strings.Contains(out.String(), `level=INFO msg="explicit interrupt" event=interrupt`) {
		t.Fatalf("custom journal = %q", out.String())
	}
}

// overlapWriter fails if two Write calls ever overlap.
type overlapWriter struct {
	active  int32
	overlap int32
	lines   int32
}

func (o *overlapWriter) Write(p []byte) (int, error) {
	if atomic.AddInt32(&o.active, 1) != 1 {
		atomic.StoreInt32(&o.overlap, 1)
	}
	time.Sleep(50 * time.Microsecond)
	atomic.AddInt32(&o.lines, 1)
	atomic.AddInt32(&o.active, -1)
	return len(p), nil
}

// The journal branch and the log package share one lock, so their lines never interleave
// (as when the log package's own lock was the only one).
func TestJournalAndLogPackageNeverWriteConcurrently(t *testing.T) {
	ow := &overlapWriter{}
	loggingState(t, ow, 0, "")
	restore := InstallAuditTee(NewAuditRing())
	defer restore()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				if i%2 == 0 {
					log.Printf("plain %d", n)
				} else {
					slog.Info("explicit interrupt", "event", "interrupt", "n", n)
				}
			}
		}(i)
	}
	within(t, "the loggers", 20*time.Second, wg.Wait)
	if atomic.LoadInt32(&ow.overlap) != 0 || atomic.LoadInt32(&ow.lines) != 240 {
		t.Fatalf("overlap=%d lines=%d, want no overlap and 240 lines", ow.overlap, ow.lines)
	}
}
