package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
		fail bool
	}{
		{"file", map[string]string{"CIRAK_TOKEN_FILE": path}, "secret-value", false},
		{"environment", map[string]string{"CIRAK_TOKEN": "inline"}, "inline", false},
		{"both", map[string]string{"CIRAK_TOKEN": "inline", "CIRAK_TOKEN_FILE": path}, "", true},
		{"relative", map[string]string{"CIRAK_TOKEN_FILE": "secret"}, "", true},
		{"missing", map[string]string{"CIRAK_TOKEN_FILE": path + "-missing"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readSecret(func(k string) string { return tc.env[k] }, "CIRAK_TOKEN")
			if (err != nil) != tc.fail || (!tc.fail && got != tc.want) {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(func(k string) string {
		if k == "CIRAK_TOKEN_FILE" {
			return path
		}
		return ""
	}, "CIRAK_TOKEN"); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "one\ntwo", "bad\x00value", strings.Repeat("x", 16385)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecret(func(k string) string {
			if k == "CIRAK_TOKEN_FILE" {
				return path
			}
			return ""
		}, "CIRAK_TOKEN"); err == nil {
			t.Fatalf("accepted malformed credential of length %d", len(bad))
		}
	}
}
