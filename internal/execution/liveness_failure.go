package execution

// ADR-017 A2 S3 (A2.4 item 1): the native-read failure class and the
// unavailable_since window that produces HARNESS_UNREACHABLE. Only a transport
// or connection error, a read timeout (including HTTP 408) or a 5xx counts; a
// 3xx, any other 4xx, a decode failure, an over-size response, a local
// filesystem timeout, an identity mismatch or a cancellation never counts.

import (
	"context"
	"errors"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

type nativeFailure int

const (
	nativeRefused nativeFailure = iota
	nativeUnavailable
	nativeLocalFS
	nativeCanceled
)

// classifyNativeRead classifies one failed native read. Cancellation is checked
// first (a canceled/expired probe is not evidence about the harness); the
// caller's context and the manager's shutdown context both count.
func classifyNativeRead(ctx, mctx context.Context, err error) nativeFailure {
	if ctx.Err() != nil || mctx.Err() != nil {
		return nativeCanceled
	}
	if errors.Is(err, errFSTimeout) {
		return nativeLocalFS
	}
	var h *opencode.HTTPError
	if errors.As(err, &h) {
		if h.Status == 408 || h.Status >= 500 {
			return nativeUnavailable
		}
		// 3xx, 401/403/404, 429 and every other 4xx: reachable but unusable.
		return nativeRefused
	}
	switch {
	case errors.Is(err, opencode.ErrTransport), errors.Is(err, opencode.ErrResponseRead):
		return nativeUnavailable
	default:
		// Decode, over-size, unrecognized and any other error: never a guess.
		return nativeRefused
	}
}

// nativeFailureLiveness is the report for one failed probe. It classifies the
// error (only when the probe failed at a native read), maintains the
// unavailable_since window and returns HARNESS_UNREACHABLE once a counting
// failure has persisted for unavailableThreshold. A non-counting failure is its
// own state and clears the window. Caller holds run.mu. The report-only rule
// applies: with the report switched off no memory is written.
func (m *Manager) nativeFailureLiveness(x *run, ctx context.Context, reason string, err error) *Liveness {
	now := m.clock().UTC()
	l := unavailableLiveness(now, m.thresholdsObject())
	if m.noLiveness {
		return l
	}
	counting := false
	if reason == "OBSERVATION_UNAVAILABLE" && err != nil {
		counting = classifyNativeRead(ctx, m.ctx, err) == nativeUnavailable
	}
	if !counting {
		x.unavailableSince = time.Time{}
		x.view.UnavailableSince = nil
		return l
	}
	if x.unavailableSince.IsZero() {
		x.unavailableSince = now
	}
	since := x.unavailableSince
	x.view.UnavailableSince = &since
	// A2.4 item 1: consecutive counting probes over unavailable_s (120 s). The
	// boundary is "at or after" the fixed window (test plan item 9: for 120 s).
	if now.Sub(since) >= unavailableThreshold {
		l.StallReason = "HARNESS_UNREACHABLE"
		l.StallSuspected = true
	}
	return l
}
