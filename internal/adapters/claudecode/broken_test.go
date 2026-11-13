package claudecode

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// replayStream feeds a raw stdout through the real decoder. When the decoder
// stops on a bound it feeds StepStreamBroken, as the runner will, then EOF and
// the exit status.
func replayStream(t *testing.T, stdout string, lim Limits, exitCode int) (Verdict, error) {
	t.Helper()
	d := NewDecoder(strings.NewReader(stdout), lim)
	m := NewMachine(testCfg())
	var stop error
	for {
		e, err := d.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				stop = err
				m.Feed(Step{Kind: StepStreamBroken})
			}
			break
		}
		m.Feed(Step{Kind: StepLine, Event: e})
	}
	m.Feed(eof())
	m.Feed(exit(exitCode))
	return m.Verdict(), stop
}

// A stream that ends on a bound AFTER the final result is row 8 (any non-empty
// line after the final result counts, decodable or not; the line that broke the
// stream was never decoded). It must never be completed with the guard released.
func TestStreamBrokenAfterTheFinalResultIsRow8(t *testing.T) {
	initL, resL := initJSON(nil)+"\n", resultJSON(nil)+"\n"
	t.Run("oversized line after a success result", func(t *testing.T) {
		v, stop := replayStream(t, initL+resL+strings.Repeat("x", 5000)+"\n", Limits{MaxLine: 4096}, 0)
		if !errors.Is(stop, ErrLineTooLong) {
			t.Fatalf("test bug: decoder stopped with %v", stop)
		}
		assertVerdict(t, v, "8", "unknown", "", false)
		if v.Detail != "stream_broken_after_result" {
			t.Fatalf("detail = %q", v.Detail)
		}
	})
	t.Run("total bound reached after a success result", func(t *testing.T) {
		pre := int64(len(initL) + len(resL))
		v, stop := replayStream(t, initL+resL+`{"type":"assistant"}`+"\n"+`{"type":"assistant"}`+"\n", Limits{MaxLine: 4096, MaxTotal: pre + 10}, 0)
		if !errors.Is(stop, ErrStreamTooLarge) {
			t.Fatalf("test bug: decoder stopped with %v", stop)
		}
		assertVerdict(t, v, "8", "unknown", "", false)
	})
	t.Run("init, result, broken, EOF, exit 0 (bare)", func(t *testing.T) {
		v := Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(nil)), {Kind: StepStreamBroken}, eof(), exit(0)})
		assertVerdict(t, v, "8", "unknown", "", false)
	})
	t.Run("also the row-2 shape (no init) and a hung or failed exit", func(t *testing.T) {
		v := Decide(testCfg(), []Step{line(notFoundJSON(testUUID)), {Kind: StepStreamBroken}, eof(), exit(1)})
		assertVerdict(t, v, "8", "unknown", "", false)
		v = Decide(testCfg(), []Step{line(initJSON(nil)), line(abortedJSON()), userInt, {Kind: StepStreamBroken}, eof(), exit(0)})
		assertVerdict(t, v, "8", "unknown", "", false)
	})
	t.Run("the same stream without a break is still completed (control)", func(t *testing.T) {
		v, stop := replayStream(t, initL+resL, Limits{MaxLine: 4096}, 0)
		if stop != nil {
			t.Fatal(stop)
		}
		assertVerdict(t, v, "3", "completed", "", true)
	})
	t.Run("a broken stream before any result stays row 6", func(t *testing.T) {
		v, stop := replayStream(t, initL+strings.Repeat("x", 5000)+"\n", Limits{MaxLine: 4096}, 0)
		if !errors.Is(stop, ErrLineTooLong) {
			t.Fatalf("test bug: %v", stop)
		}
		assertVerdict(t, v, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false)
	})
	t.Run("a latch still wins over the break", func(t *testing.T) {
		v := Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(nil)), graceEnd, {Kind: StepStreamBroken}, eof(), exit(0)})
		assertVerdict(t, v, "6a", "unknown", "EXIT_UNCONFIRMED", false)
	})
}

// P3-7 decision: whitespace-only lines are padding, not events; the decoder
// skips them and they do not count for row 8. A non-blank line always counts.
func TestWhitespaceOnlyLinesAfterTheResultAreIgnored(t *testing.T) {
	v, stop := replayStream(t, initJSON(nil)+"\n"+resultJSON(nil)+"\n\n   \n\t\n", Limits{}, 0)
	if stop != nil {
		t.Fatal(stop)
	}
	assertVerdict(t, v, "3", "completed", "", true)
	v, _ = replayStream(t, initJSON(nil)+"\n"+resultJSON(nil)+"\n \n.\n", Limits{}, 0)
	assertVerdict(t, v, "8", "unknown", "", false)
}
