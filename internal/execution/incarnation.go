package execution

import "context"

// ADR-024 Amendment A1, slice S7 (D21): the run's submit epoch.
//
// Every run records the continuity epoch that was ARMED when it was admitted (run.submitEpoch, Bridge
// memory only, ADR-010); "" means none: no incarnation source, an unarmed tracker or a failed read, and a
// run with no submit epoch can never be verified. The authority supplies NativeEpoch only when the
// source's current epoch is armed AND equal to the run's submit epoch (runAuthority.PermissionScope), so a
// run submitted before a dispose, a reload, a reconnect or a heartbeat gap is unverifiable afterwards
// (the human answers natively), and so is a run submitted while the epoch was not armed.
//
// The epoch is only an extra condition: every check of ADR-024 4.2 and ADR-018 section 5 still has to
// hold, and nothing here touches reject-only (D15) or the refusal of allow_once.

type incarnationBox struct{ inc NativeIncarnation }

// SetIncarnation installs the incarnation source the manager reads at admission (the same source the
// PermissionSource is configured with). A nil source removes it, so every later run has no submit epoch.
// It is set once at wiring time, before any run is admitted.
func (m *Manager) SetIncarnation(inc NativeIncarnation) {
	if inc == nil {
		m.incarnation.Store(nil)
		return
	}
	m.incarnation.Store(&incarnationBox{inc: inc})
}

// admissionEpoch returns the epoch armed now, or "" (no source, not armed, or an error: fail closed).
func (m *Manager) admissionEpoch(ctx context.Context) string {
	box := m.incarnation.Load()
	if box == nil {
		return ""
	}
	epoch, err := box.inc.NativeEpoch(ctx)
	if err != nil {
		return ""
	}
	return epoch
}
