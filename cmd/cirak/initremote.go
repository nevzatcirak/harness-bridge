package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nevzatcirak/cirak/internal/security"
)

// The remote/TLS path of `cirak init` (ADR-027 section 4, slice S4). Remote mode needs the Bridge's own TLS
// certificate and key (a missing half is refused, never a plaintext fallback), an HTTPS public origin, a named
// principal and an explicit token deadline: the daemon's own constructor (security.NewRemoteWithMaxLifetime)
// decides the last three, so the wizard cannot accept what a start would refuse. Everything here runs before
// OpenCode is probed or anything is written. init never opens a firewall, edits a proxy, or applies a fix: where a
// file is not usable it prints the exact command and stops.

// parseCertificateNotAfter reads the first PEM certificate of data and returns its expiry. The errors are the
// words doctor and init put after the flag name; neither includes certificate content.
func parseCertificateNotAfter(data []byte) (time.Time, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, errors.New("holds no PEM certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, errors.New("is not a valid certificate")
	}
	return c.NotAfter, nil
}

// checkRemote validates the remote-mode answers; it is a no-op for a local setup.
func (w *wizard) checkRemote() error {
	if !w.o.remote {
		return nil
	}
	o := w.o
	maxLife, err := time.ParseDuration(tokenMaxLife)
	if err != nil {
		return err
	}
	// The daemon's own rule for the origin, the principal and the deadline (messages never echo a value).
	if _, err := security.NewRemoteWithMaxLifetime(o.publicOrigin, o.principal, w.expires, maxLife); err != nil {
		return usagef("remote mode: %v", err)
	}
	for name, p := range map[string]string{"--tls-cert": o.tlsCert, "--tls-key": o.tlsKey} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return usagef("%s must be an absolute canonical path", name)
		}
		if err := noSymlinkInExistingPrefix(p); err != nil {
			return usagef("%s: %v", name, err)
		}
	}
	certInfo, err := os.Lstat(o.tlsCert)
	if err != nil || !certInfo.Mode().IsRegular() {
		return usagef("--tls-cert %s must be an existing regular file", o.tlsCert)
	}
	data, err := readNoFollow(o.tlsCert)
	if err != nil {
		return usagef("--tls-cert %s is unreadable", o.tlsCert)
	}
	notAfter, err := parseCertificateNotAfter(data)
	if err != nil {
		return usagef("--tls-cert %s %v", o.tlsCert, err)
	}
	switch left := notAfter.Sub(w.d.now()); {
	case left <= 0:
		return usagef("the TLS certificate expired on %s: renew it first (nothing was written)", notAfter.UTC().Format("2006-01-02"))
	case left < 14*24*time.Hour:
		w.say("warning: the TLS certificate expires on %s", notAfter.UTC().Format("2006-01-02"))
	}
	keyInfo, err := os.Lstat(o.tlsKey)
	if err != nil || !keyInfo.Mode().IsRegular() {
		return usagef("--tls-key %s must be an existing regular file (not a symlink)", o.tlsKey)
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		w.say("fix (init never applies it): chmod 0600 %s", shq(o.tlsKey))
		return usagef("--tls-key %s must be private (mode 0600); nothing was written", o.tlsKey)
	}
	var bad bool
	if !w.serviceCanRead(o.tlsCert, certInfo) {
		w.say("--tls-cert %s: %s needs read on it", o.tlsCert, w.svc.Name)
		w.say("  fix (init never applies it): setfacl -m %s %s", shq("u:"+w.svc.Name+":r"), shq(o.tlsCert))
		bad = true
	}
	if !w.serviceCanRead(o.tlsKey, keyInfo) {
		w.say("--tls-key %s: %s needs read on it", o.tlsKey, w.svc.Name)
		w.say("  fix (init never applies it): chown %s %s && chmod 0600 %s", shq(w.svc.Name+":"+w.svc.Group), shq(o.tlsKey), shq(o.tlsKey))
		bad = true
	}
	if bad {
		return errors.New("the TLS files cannot be read by the service user as they are; nothing was written")
	}
	w.say("Remote mode: the Bridge needs both the TLS certificate and key (a missing half is refused, never a plaintext fallback).")
	w.say("  A reverse proxy may terminate the client TLS and forward over verified TLS to this backend: see docs/deploy/Caddyfile.example and docs/deploy/PRIVATE_HTTPS_PREPARATION.md. init never opens a firewall or edits a proxy.")
	return nil
}
