package security

import "testing"

func TestListenPolicy(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		for _, remote := range []bool{false, true} {
			if err := ValidateListen(addr, remote); err != nil {
				t.Fatal(addr, remote, err)
			}
		}
	}
	for _, addr := range []string{"172.18.0.1:8443", "10.1.2.3:8443", "192.168.1.1:8443", "[fd00::1]:8443"} {
		if err := ValidateListen(addr, false); err == nil {
			t.Fatal("private plaintext bind accepted", addr)
		}
		if err := ValidateListen(addr, true); err != nil {
			t.Fatal(addr, err)
		}
	}
	for _, addr := range []string{
		"91.99.201.115:8443", "8.8.8.8:8443", "[2606:4700:4700::1111]:8443",
		":8443", "0.0.0.0:8443", "[::]:8443", "bridge.nevzatcirak.com:8443",
		"169.254.1.1:8443", "[fe80::1%eth0]:8443", "[fd00::1%eth0]:8443",
		"[::ffff:172.18.0.1]:8443", "224.0.0.1:8443", "100.64.0.1:8443",
		"172.32.0.1:8443", "172.15.255.255:8443", "172.18.0.1/16:8443",
		"127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:https", "127.0.0.1", "127.0.0.1:-1",
	} {
		for _, remote := range []bool{false, true} {
			if err := ValidateListen(addr, remote); err == nil {
				t.Fatal("forbidden bind accepted", addr, remote)
			}
		}
	}
}
