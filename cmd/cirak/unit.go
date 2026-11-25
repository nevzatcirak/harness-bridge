package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Service files of `cirak init --install-service` (ADR-027 section 2 item 5): the systemd unit (a full unit, or a
// drop-in over the packaged one) and the launchd plists. Everything here is pure text; nothing is written,
// installed, started or loaded by this file.

const (
	// defaultUnitBinary is the ExecStart of the unit: bare `cirak` and `cirak serve` are the same command.
	defaultUnitBinary = "/usr/bin/cirak"
	unitName          = "cirak.service"
	dropInName        = "zz-cirak-init.conf"
	// credentialName is the ADR-026 section 4.4 credential name: LoadCredential=token:<file> delivers it, and the
	// config spells it token.credential: "token".
	credentialName = "token"
	agentLabel     = "dev.cirak.agent"
	daemonLabel    = "dev.cirak.daemon"
)

var (
	// systemUnitDir, packagedUnitPath and the launchd directories are variables only so tests can use temp
	// directories; production never changes them.
	systemUnitDir    = "/etc/systemd/system"
	packagedUnitPath = "/usr/lib/systemd/system/cirak.service"
	launchDaemonDir  = "/Library/LaunchDaemons"
)

// unitSafe is the set of characters a value may hold to be written into a unit without quoting rules: a path or
// a user name with a space, a quote, a backslash, a % specifier or a $ expansion is refused, never escaped.
var unitSafe = regexp.MustCompile(`^[A-Za-z0-9._/+@:,=-]+$`)

type unitOptions struct {
	User, Group string
	ConfigPath  string // CIRAK_CONFIG
	TokenPath   string // the file LoadCredential= reads
	Binary      string // ExecStart; defaults to /usr/bin/cirak
	// ClaudeAdapter is true when the Claude Code adapter runs: its child runs under `sudo -u hbclaude`, sudo is
	// setuid, and NoNewPrivileges=yes would break every run. ClaudeHome is the adapter's HOME (optional).
	ClaudeAdapter bool
	ClaudeHome    string
}

func (o unitOptions) check() error {
	for name, v := range map[string]string{"user": o.User, "group": o.Group, "config path": o.ConfigPath, "token path": o.TokenPath} {
		if !unitSafe.MatchString(v) {
			return fmt.Errorf("the %s cannot be written into a unit file (letters, digits and . _ / + @ : , = - only)", name)
		}
	}
	if o.Binary != "" && !unitSafe.MatchString(o.Binary) {
		return errors.New("the binary path cannot be written into a unit file")
	}
	if o.ClaudeHome != "" && !unitSafe.MatchString(o.ClaudeHome) {
		return errors.New("the Claude home path cannot be written into a unit file")
	}
	return nil
}

func (o unitOptions) binary() string {
	if o.Binary != "" {
		return o.Binary
	}
	return defaultUnitBinary
}

// privilegeLines is the hardening that depends on the Claude adapter, with the reason written next to it. With
// the adapter off: NoNewPrivileges=yes. With it on: no NoNewPrivileges (a drop-in sets it to no, to override the
// packaged unit), and ProtectHome=off, plus ReadWritePaths= for the adapter's HOME when it is known.
func (o unitOptions) privilegeLines(dropIn bool) []string {
	if !o.ClaudeAdapter {
		if dropIn {
			return nil
		}
		return []string{"NoNewPrivileges=yes"}
	}
	lines := []string{
		"# Claude Code adapter ON: its child runs under sudo -u hbclaude, sudo is setuid and NoNewPrivileges=yes would",
		"# break every run, so it is not set. ProtectHome=off is the chosen form (ReadWritePaths= names the adapter HOME).",
	}
	if dropIn {
		lines = append(lines, "NoNewPrivileges=no")
	}
	lines = append(lines, "ProtectHome=off")
	if o.ClaudeHome != "" {
		lines = append(lines, "ReadWritePaths="+o.ClaudeHome)
	}
	return lines
}

const unitHeader = `# Written by "cirak init" (ADR-027 section 2 item 5). Nothing in it is a secret.
# Proven live on the H5 host (F-H5-3): LoadCredential=, RuntimeDirectory=, StateDirectory=.
# Proposed hardening, NOT yet verified on a live host: ProtectSystem=, PrivateTmp=, RestrictAddressFamilies=,
# NoNewPrivileges=.
`

// renderFullUnit is /etc/systemd/system/cirak.service for a host with no packaged unit.
func renderFullUnit(o unitOptions) (string, error) {
	if err := o.check(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	b.WriteString("\n[Unit]\nDescription=Cirak: the MCP bridge to existing OpenCode sessions\nAfter=network.target\n\n[Service]\n")
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nExecStart=%s\n", o.User, o.Group, o.binary())
	fmt.Fprintf(&b, "Environment=CIRAK_CONFIG=%s\n", o.ConfigPath)
	b.WriteString("# proven live:\n")
	b.WriteString("StateDirectory=cirak cirak/writer-locks cirak/overlay\nStateDirectoryMode=0700\n")
	b.WriteString("RuntimeDirectory=cirak-admin cirak-dashboard\nRuntimeDirectoryMode=0700\n")
	fmt.Fprintf(&b, "LoadCredential=%s:%s\n", credentialName, o.TokenPath)
	b.WriteString("# proposed:\n")
	b.WriteString("ProtectSystem=strict\nPrivateTmp=yes\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\n")
	for _, l := range o.privilegeLines(false) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String(), nil
}

// renderDropIn is /etc/systemd/system/cirak.service.d/zz-cirak-init.conf over the packaged unit
// (/usr/lib/systemd/system/cirak.service): it adds or overrides only what this install needs.
func renderDropIn(o unitOptions) (string, error) {
	if err := o.check(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	b.WriteString("# A drop-in over the packaged unit: only what this install needs. The packaged unit already runs as the\n# cirak user and reads the credential below as its token file.\n")
	b.WriteString("\n[Service]\n")
	fmt.Fprintf(&b, "Environment=CIRAK_CONFIG=%s\n", o.ConfigPath)
	b.WriteString("# the credential list is reset, then this host's token file is the source of the credential \"token\"\n")
	fmt.Fprintf(&b, "LoadCredential=\nLoadCredential=%s:%s\n", credentialName, o.TokenPath)
	for _, l := range o.privilegeLines(true) {
		b.WriteString(l + "\n")
	}
	return b.String(), nil
}

type plistOptions struct {
	Label      string
	Binary     string
	ConfigPath string
	LogDir     string
	UserName   string // LaunchDaemon only: the run user
}

// renderPlist is a launchd property list: KeepAlive, RunAtLoad, logs under LogDir and CIRAK_CONFIG in the
// environment. There is no LoadCredential equivalent on macOS: the token is a 0600 file of the run user.
func renderPlist(o plistOptions) (string, error) {
	for name, v := range map[string]string{"label": o.Label, "binary": o.Binary, "config path": o.ConfigPath, "log directory": o.LogDir} {
		if v == "" || strings.ContainsAny(v, "\n\r\x00") {
			return "", fmt.Errorf("the %s cannot be written into a property list", name)
		}
	}
	esc := func(s string) string {
		var buf bytes.Buffer
		_ = xml.EscapeText(&buf, []byte(s))
		return buf.String()
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<!-- Written by \"cirak init\" (ADR-027 section 2 item 5). Nothing in it is a secret. -->\n<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", esc(o.Label))
	if o.UserName != "" {
		fmt.Fprintf(&b, "\t<key>UserName</key>\n\t<string>%s</string>\n", esc(o.UserName))
	}
	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>%s</string>\n\t</array>\n", esc(o.Binary))
	fmt.Fprintf(&b, "\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>CIRAK_CONFIG</key>\n\t\t<string>%s</string>\n\t</dict>\n", esc(o.ConfigPath))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s/cirak.log</string>\n", esc(strings.TrimSuffix(o.LogDir, "/")))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s/cirak.err.log</string>\n", esc(strings.TrimSuffix(o.LogDir, "/")))
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}
