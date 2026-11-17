//go:build !unix

package acceptance

import "errors"

var errDecoyFSUnsupported = errors.New("acceptance: the decoy filesystem needs a unix platform (fails closed)")

// Mkdir implements DecoyFS: refused on this platform.
func (o *OSDecoyFS) Mkdir(p string) error { return errDecoyFSUnsupported }

// Write implements DecoyFS: refused on this platform.
func (o *OSDecoyFS) Write(p string, content []byte) error { return errDecoyFSUnsupported }

// Remove implements DecoyFS: refused on this platform.
func (o *OSDecoyFS) Remove(p string) error { return errDecoyFSUnsupported }

// RemoveDir implements DecoyFS: refused on this platform.
func (o *OSDecoyFS) RemoveDir(p string) error { return errDecoyFSUnsupported }
