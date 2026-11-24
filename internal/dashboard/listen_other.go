//go:build !linux

package dashboard

import (
	"fmt"
	"net"
)

// listenUnix is only implemented on Linux, like the admin socket.
func listenUnix(string, int) (net.Listener, error) {
	return nil, fmt.Errorf("CIRAK_DASHBOARD_SOCKET is supported on Linux only; use CIRAK_DASHBOARD_ADDR with a loopback address")
}
