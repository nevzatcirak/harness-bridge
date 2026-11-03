package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCredentialACL(t *testing.T) {
	b := make([]byte, 44)
	binary.LittleEndian.PutUint32(b, 2)
	for i, tag := range []uint16{1, 2, 4, 16, 32} {
		e := b[4+i*8 : 12+i*8]
		binary.LittleEndian.PutUint16(e, tag)
		if tag == 1 || tag == 2 || tag == 16 {
			binary.LittleEndian.PutUint16(e[2:], 4)
		}
		id := uint32(0xffffffff)
		if tag == 2 {
			id = 996
		}
		binary.LittleEndian.PutUint32(e[4:], id)
	}
	if !validCredentialACL(b, 996, 4) {
		t.Fatal("valid exact ACL rejected")
	}
	for i := range b {
		bad := append([]byte(nil), b...)
		bad[i] ^= 1
		if validCredentialACL(bad, 996, 4) {
			t.Fatalf("modified ACL accepted at byte %d", i)
		}
	}
	if validCredentialACL(append(b, make([]byte, 8)...), 996, 4) || validCredentialACL(b, 995, 4) {
		t.Fatal("extra grant or wrong identity accepted")
	}
}

func TestSecretFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		mode       os.FileMode
	}{
		{"symlink", link, 0600}, {"directory", dir, 0600}, {"fifo", fifo, 0600},
		{"group-readable", path, 0640}, {"world-readable", path, 0644}, {"forged-credential-directory", path, 0440},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path == path {
				if err := os.Chmod(path, tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readSecret(func(k string) string {
				if k == "BRIDGE_TOKEN_FILE" {
					return tc.path
				}
				if k == "CREDENTIALS_DIRECTORY" {
					return dir
				}
				return ""
			}, "BRIDGE_TOKEN")
			if err == nil {
				t.Fatal("unsafe credential accepted")
			}
		})
	}
}

// Explicit opt-in: executed separately under genuine systemd LoadCredential
// using a synthetic fixture, never a production credential.
func TestSystemdManagedCredential(t *testing.T) {
	if os.Getenv("HB_SYSTEMD_CREDENTIAL_TEST") != "1" {
		t.Skip("requires systemd LoadCredential fixture")
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	value, err := readSecret(func(k string) string {
		if k == "BRIDGE_TOKEN_FILE" {
			return filepath.Join(dir, "fixture")
		}
		return os.Getenv(k)
	}, "BRIDGE_TOKEN")
	if err != nil || value != "synthetic-systemd-fixture" {
		t.Fatal("systemd credential read failed (value withheld)", err)
	}
}
