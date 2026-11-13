//go:build !linux

package runner

import "errors"

// ExecSpawner is unsupported off linux: Start always fails, so no child is ever
// started and the run is a pre-submission failure.
type ExecSpawner struct{}

// Start implements Spawner.
func (ExecSpawner) Start(Spec) (Child, error) {
	return nil, errors.New("runner: starting the Claude Code wrapper is supported on linux only")
}
