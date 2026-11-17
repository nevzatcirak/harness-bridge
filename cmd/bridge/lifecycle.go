package main

import (
	"fmt"
	"strings"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/overlay"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 / ADR-025 production wiring (maintainer decision D22): the merged lifecycle surface is
// enabled by default, with no extra enable switch, and is inert until the operator configures it.
//
//	BRIDGE_LIFECYCLE_CONFIG  path of the lifecycle configuration (internal/lifecycle: roots, modes, caps).
//	BRIDGE_OVERLAY_DIR       directory of the archive overlay (ADR-025 4.1, D18 Q5/Q6).
//
// Unset BRIDGE_LIFECYCLE_CONFIG: no lifecycle tool, exactly the previous tool set. Set and valid:
// list_catalog and create_session are registered; with a valid BRIDGE_OVERLAY_DIR the three archive
// tools too. Invalid configuration is a refusal to start, never "start without it". BRIDGE_READ_ONLY
// registers list_catalog only (the overlay is then opened without the write role): create_session and
// the archive tools never exist on a read-only ingress. ADR-012 authorization and the per-session
// ACL are untouched: the lifecycle roots grant nothing to the existing tools (ADR-022 3.2).

const envOverlayDir = "BRIDGE_OVERLAY_DIR"

// lifecycleWiring is the validated lifecycle state of one process. A nil *lifecycleWiring means the
// feature is not configured.
type lifecycleWiring struct {
	catalog *mcp.CatalogConfig
	create  *mcp.CreateConfig  // nil on a read-only ingress
	archive *mcp.ArchiveConfig // nil without an overlay, on a read-only ingress, or without the write lock
	store   *overlay.Store     // nil without BRIDGE_OVERLAY_DIR
	roots   int
}

// openLifecycle resolves the configuration before any listener is opened. uid is the service user.
func openLifecycle(env func(string) string, uid int, readOnly bool) (*lifecycleWiring, error) {
	cfg, err := lifecycle.FromEnv(env, uid)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lifecycle.EnvConfig, err)
	}
	dir := env(envOverlayDir)
	if cfg == nil {
		if dir != "" {
			return nil, fmt.Errorf("%s is set but %s is not: the archive overlay needs lifecycle roots, refusing to start without them", envOverlayDir, lifecycle.EnvConfig)
		}
		return nil, nil
	}
	if !cfg.Enabled() {
		return nil, fmt.Errorf("%s names no root: configure at least one root or unset the variable", lifecycle.EnvConfig)
	}
	paths := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		paths = append(paths, r.Path)
	}
	roots, err := security.NewLifecycleRoots(paths)
	if err != nil {
		return nil, fmt.Errorf("lifecycle roots: %w", err)
	}
	w := &lifecycleWiring{catalog: &mcp.CatalogConfig{Roots: roots}, roots: len(cfg.Roots)}
	fail := func(err error) (*lifecycleWiring, error) {
		w.Close()
		return nil, err
	}
	if dir != "" {
		// The writer role is taken only by a process that can mutate (ADR-025 4.3, D18 Q3): an invalid
		// file is then a startup error. A read-only process opens the directory as a reader.
		store, err := overlay.Open(dir, overlay.Options{Writer: !readOnly})
		if err != nil {
			return fail(fmt.Errorf("%s: %w", envOverlayDir, err))
		}
		w.store = store
		w.catalog.Overlay = store
	}
	if readOnly {
		return w, nil
	}
	create, err := mcp.NewCreateConfig(cfg)
	if err != nil {
		return fail(fmt.Errorf("create_session: %w", err))
	}
	w.create = create
	// A writer that did not get the overlay lock behaves as a reader (ADR-025 4.3): it keeps the
	// catalog and create tools, and registers no archive tool.
	if w.store != nil && w.store.CanWrite() {
		archive, err := mcp.NewArchiveConfig(cfg, w.store)
		if err != nil {
			return fail(fmt.Errorf("archive tools: %w", err))
		}
		w.archive = archive
	}
	return w, nil
}

// Close releases the overlay. It is safe on a nil wiring.
func (w *lifecycleWiring) Close() {
	if w != nil && w.store != nil {
		w.store.Close()
	}
}

// apply sets the lifecycle configuration on the one mcp.Server. It is safe on a nil wiring (nothing
// is set, so the tool set is unchanged).
func (w *lifecycleWiring) apply(s *mcp.Server, runs *execution.Manager) {
	if w == nil {
		return
	}
	s.Catalog = w.catalog
	s.Create = w.create
	s.Archive = w.archive
	if w.archive != nil {
		w.archive.Install(runs) // send_prompt refuses an archived session
	}
}

// tools names the lifecycle tools this wiring registers.
func (w *lifecycleWiring) tools() []string {
	if w == nil {
		return nil
	}
	out := []string{"list_catalog"}
	if w.create != nil {
		out = append(out, "create_session")
	}
	if w.archive != nil {
		out = append(out, "archive_session", "unarchive_session", "close_session")
	}
	return out
}

// summary is the startup line: the registered lifecycle tools and the root count. It never names a
// path, a variable value or a secret.
func (w *lifecycleWiring) summary(readOnly bool) string {
	if w == nil {
		return "lifecycle: not configured, no lifecycle tools registered (roots=0)"
	}
	s := fmt.Sprintf("lifecycle: registered %s (roots=%d, overlay=%t)", strings.Join(w.tools(), ","), w.roots, w.store != nil)
	if readOnly {
		s += "; read-only ingress: no mutating lifecycle tool"
	}
	return s
}
