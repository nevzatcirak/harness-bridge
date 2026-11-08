//go:build !linux

package admin

import (
	"fmt"
	"net"
)

// Listen requires Linux (SO_PEERCRED).
func Listen(string, int) (net.Listener, error) {
	return nil, fmt.Errorf("the admin socket requires Linux")
}

func peerUID(net.Conn) (int, error) { return 0, fmt.Errorf("peer credentials require Linux") }
