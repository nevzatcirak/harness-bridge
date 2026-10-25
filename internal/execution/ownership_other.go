//go:build !linux

package execution

import "fmt"

type Ownership struct{}

func AcquireOwnership(string, []string) (*Ownership, error) {
	return nil, fmt.Errorf("writer ownership requires Linux; use read-only mode on this platform")
}
func (*Ownership) Close() {}
