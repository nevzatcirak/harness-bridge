package acceptance

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ProdObserver is the production Observer of the R6 live acceptance (ADR-021 §8.3, ACCEPTANCE_S3.md
// item 1): the three checks that are not in the NDJSON stream. Each component is optional; a
// component that is nil observes nothing (observed=false), so a missing flag can only ever make item 1
// INCOMPLETE, never PASS. No component fabricates a value: any error is `observed=false`.
//
//   - Tap: the Bridge-side history record of the denied runs (see HistoryTap);
//   - Scan: the entries of the canary directory the hostile root writes into (see FileCanaryScan);
//   - Sink: the requests a loopback listener received (see LoopbackSink).
type ProdObserver struct {
	Tap  *HistoryTap
	Scan *FileCanaryScan
	Sink *LoopbackSink
}

// History implements Observer.
func (o *ProdObserver) History(ctx context.Context) ([]byte, bool) {
	if o == nil || o.Tap == nil {
		return nil, false
	}
	return o.Tap.Read()
}

// FileCanaries implements Observer.
func (o *ProdObserver) FileCanaries(ctx context.Context) (int, bool) {
	if o == nil || o.Scan == nil {
		return 0, false
	}
	return o.Scan.Count()
}

// SinkRequests implements Observer.
func (o *ProdObserver) SinkRequests(ctx context.Context) (int, bool) {
	if o == nil || o.Sink == nil {
		return 0, false
	}
	return o.Sink.Count()
}

// Close releases every component (the tap's directory descriptor, the sink's listener).
func (o *ProdObserver) Close() error {
	if o == nil {
		return nil
	}
	var errs []error
	if o.Tap != nil {
		errs = append(errs, o.Tap.Close())
	}
	if o.Sink != nil {
		errs = append(errs, o.Sink.Close())
	}
	if o.Scan != nil {
		errs = append(errs, o.Scan.Close())
	}
	return errors.Join(errs...)
}

var _ Observer = (*ProdObserver)(nil)

// ObserverOptions are the flags of the live command that switch the production Observer on. Every field
// is optional: an empty field leaves its check unobserved (item 1 then stays INCOMPLETE), so nothing
// can pass silently because a flag was left out.
type ObserverOptions struct {
	HistoryDir string // a dedicated 0700 directory for the history store of the denied runs
	CanaryDir  string // the empty directory the hostile root's hook, apiKeyHelper and MCP server write into
	SinkAddr   string // a loopback "IP:port" for the request sink ("127.0.0.1:0" = an ephemeral port)
	RunHome    string // the run user's HOME (the canary directory may not touch its denied directories)
}

// Enabled reports whether any component was asked for.
func (o ObserverOptions) Enabled() bool {
	return o.HistoryDir != "" || o.CanaryDir != "" || o.SinkAddr != ""
}

// BuildObserver assembles the production Observer from the options. With nothing configured it
// returns a nil observer and the inner runner unchanged. When the history component is on, the
// returned runner tees the denied runs into it. On any error everything already opened is closed.
func BuildObserver(o ObserverOptions, inner Runner) (*ProdObserver, Runner, error) {
	if !o.Enabled() {
		return nil, inner, nil
	}
	obs := &ProdObserver{}
	runner := inner
	fail := func(err error) (*ProdObserver, Runner, error) {
		_ = obs.Close()
		return nil, inner, err
	}
	if o.HistoryDir != "" {
		tap, err := NewHistoryTap(o.HistoryDir)
		if err != nil {
			return fail(fmt.Errorf("history directory refused: %w", err))
		}
		obs.Tap = tap
		runner = RecordingRunner{Inner: inner, Tap: tap}
	}
	if o.CanaryDir != "" {
		scan, err := NewFileCanaryScan(o.CanaryDir, o.RunHome)
		if err != nil {
			return fail(fmt.Errorf("canary directory refused: %w", err))
		}
		obs.Scan = scan
	}
	if o.SinkAddr != "" {
		sink, err := NewLoopbackSink(o.SinkAddr)
		if err != nil {
			return fail(fmt.Errorf("sink refused: %w", err))
		}
		obs.Sink = sink
	}
	return obs, runner, nil
}

// Describe returns the evidence pairs of the observer's configuration: which checks are on and the
// sink's port, never a path or a URL.
func (o *ProdObserver) Describe() []string {
	onoff := func(b bool) string {
		if b {
			return "configured"
		}
		return "none"
	}
	pairs := []string{"history", onoff(o != nil && o.Tap != nil), "file_canaries", onoff(o != nil && o.Scan != nil), "sink", onoff(o != nil && o.Sink != nil)}
	if o != nil && o.Sink != nil {
		if _, port, err := net.SplitHostPort(o.Sink.ln.Addr().String()); err == nil {
			pairs = append(pairs, "sink_port", port)
		}
	}
	return pairs
}
