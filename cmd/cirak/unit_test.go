package main

import (
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files of the rendered units")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "units", name)
	if *updateGolden {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create the golden)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// directives is a unit without its comment lines: assertions about keys must not be satisfied (or tripped) by the
// explanatory comments.
func directives(u string) string {
	var out []string
	for _, l := range strings.Split(u, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func baseUnit() unitOptions {
	return unitOptions{User: "cirak", Group: "cirak", ConfigPath: "/etc/cirak/config.json", TokenPath: "/etc/cirak/token"}
}

func TestRenderedUnitsMatchTheirGoldens(t *testing.T) {
	off, on := baseUnit(), baseUnit()
	on.ClaudeAdapter, on.ClaudeHome = true, "/home/hbclaude"
	for _, c := range []struct {
		name   string
		render func() (string, error)
	}{
		{"full.service", func() (string, error) { return renderFullUnit(off) }},
		{"full-claude.service", func() (string, error) { return renderFullUnit(on) }},
		{"dropin.conf", func() (string, error) { return renderDropIn(off) }},
		{"dropin-claude.conf", func() (string, error) { return renderDropIn(on) }},
		{"agent.plist", func() (string, error) {
			return renderPlist(plistOptions{Label: agentLabel, Binary: "/usr/local/bin/cirak", ConfigPath: "/Users/op/Library/Application Support/cirak/config.json", LogDir: "/Users/op/Library/Logs/cirak"})
		}},
		{"daemon.plist", func() (string, error) {
			return renderPlist(plistOptions{Label: daemonLabel, Binary: "/usr/local/bin/cirak", ConfigPath: "/etc/cirak/config.json", LogDir: "/var/log/cirak", UserName: "_cirak"})
		}},
	} {
		got, err := c.render()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		golden(t, c.name, got)
	}
}

// ADR-027 section 2 item 5 and section 6 case 5, line by line.
func TestFullUnitFollowsTheADR(t *testing.T) {
	u, err := renderFullUnit(baseUnit())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=cirak\n", "Group=cirak\n", "ExecStart=/usr/bin/cirak\n", "Environment=CIRAK_CONFIG=/etc/cirak/config.json\n",
		"StateDirectory=cirak cirak/writer-locks cirak/overlay\n", "StateDirectoryMode=0700\n",
		"RuntimeDirectory=cirak-admin cirak-dashboard\n", "RuntimeDirectoryMode=0700\n",
		"LoadCredential=token:/etc/cirak/token\n",
		"ProtectSystem=strict\n", "PrivateTmp=yes\n", "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\n",
		"NoNewPrivileges=yes\n", "WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(directives(u), want) {
			t.Errorf("the full unit lacks %q", want)
		}
	}
	// the proven/proposed split is stated, and the proven keys sit under the proven comment
	for _, want := range []string{"Proven live on the H5 host (F-H5-3): LoadCredential=, RuntimeDirectory=, StateDirectory=.", "Proposed hardening, NOT yet verified on a live host"} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit does not state %q", want)
		}
	}
	proven, proposed := strings.Index(u, "# proven live:"), strings.Index(u, "# proposed:")
	for _, k := range []string{"StateDirectory=", "RuntimeDirectory=", "LoadCredential="} {
		if i := strings.Index(u[proven:], k); i < 0 || proven+i > proposed {
			t.Errorf("%s is not in the proven block", k)
		}
	}
	// no secret, no old names, no second spelling of the start command
	for _, bad := range []string{"mcp-token", "/usr/local/bin/cirak", " serve", "CIRAK_TOKEN=", "BRIDGE_", "Environment=CIRAK_TOKEN"} {
		if strings.Contains(u, bad) {
			t.Errorf("the unit contains %q", bad)
		}
	}
	if strings.Contains(u, "StateDirectory=cirak cirak/writer-locks cirak/overlay") && strings.Contains(u, "/run/") && strings.Contains(u, "writer-locks") && strings.Contains(u, "/run/cirak/writer") {
		t.Error("the writer lock is under /run")
	}
}

func TestUnitWithTheAdapterOnHasNoNewPrivileges(t *testing.T) {
	o := baseUnit()
	o.ClaudeAdapter = true
	for name, render := range map[string]func(unitOptions) (string, error){"full": renderFullUnit, "dropin": renderDropIn} {
		u, err := render(o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(directives(u), "NoNewPrivileges=yes") {
			t.Errorf("%s: NoNewPrivileges=yes with the adapter on breaks every sudo run", name)
		}
		if !strings.Contains(directives(u), "ProtectHome=off") || !strings.Contains(u, "sudo -u hbclaude") {
			t.Errorf("%s: the chosen ProtectHome form and its reason are not stated", name)
		}
	}
	d, _ := renderDropIn(o)
	if !strings.Contains(directives(d), "NoNewPrivileges=no\n") {
		t.Error("the drop-in does not override the packaged unit's NoNewPrivileges")
	}
	o.ClaudeHome = "/home/hbclaude"
	if f, _ := renderFullUnit(o); !strings.Contains(directives(f), "ReadWritePaths=/home/hbclaude\n") {
		t.Error("the adapter HOME is not a read-write path")
	}
	// adapter off: the full unit sets it, the drop-in does not mention it
	off := baseUnit()
	if f, _ := renderFullUnit(off); !strings.Contains(directives(f), "NoNewPrivileges=yes\n") {
		t.Error("the adapter-off unit lacks NoNewPrivileges=yes")
	}
	if d, _ := renderDropIn(off); strings.Contains(directives(d), "NoNewPrivileges") || strings.Contains(directives(d), "ProtectHome") {
		t.Error("the adapter-off drop-in sets a privilege key")
	}
}

func TestDropInOnlyAddsWhatTheInstallNeeds(t *testing.T) {
	d, err := renderDropIn(baseUnit())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Service]\n", "Environment=CIRAK_CONFIG=/etc/cirak/config.json\n", "LoadCredential=\nLoadCredential=token:/etc/cirak/token\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("the drop-in lacks %q", want)
		}
	}
	for _, bad := range []string{"[Unit]", "[Install]", "ExecStart", "User=", "Group=", "StateDirectory", "ProtectSystem"} {
		if strings.Contains(directives(d), bad) {
			t.Errorf("the drop-in redefines %q (it must add only what this install needs)", bad)
		}
	}
}

func TestUnitValuesThatCannotBeWrittenAreRefused(t *testing.T) {
	for _, bad := range []string{"has space", "new\nline", "a%b", "a$b", `a"b`, "a'b", `a\b`, "", "tab\tb", "a;b", "a&b"} {
		for field, mut := range map[string]func(*unitOptions, string){
			"user": func(o *unitOptions, v string) { o.User = v }, "group": func(o *unitOptions, v string) { o.Group = v },
			"config": func(o *unitOptions, v string) { o.ConfigPath = v }, "token": func(o *unitOptions, v string) { o.TokenPath = v },
			"binary": func(o *unitOptions, v string) { o.Binary = v }, "claude home": func(o *unitOptions, v string) { o.ClaudeHome = v },
		} {
			o := baseUnit()
			mut(&o, bad)
			if bad == "" && (field == "binary" || field == "claude home") {
				continue // optional
			}
			if _, err := renderFullUnit(o); err == nil {
				t.Errorf("%s=%q accepted by the full unit", field, bad)
			}
			if _, err := renderDropIn(o); err == nil {
				t.Errorf("%s=%q accepted by the drop-in", field, bad)
			}
		}
	}
}

func TestPlistIsWellFormedAndEscaped(t *testing.T) {
	p, err := renderPlist(plistOptions{Label: agentLabel, Binary: "/opt/a&b/cirak", ConfigPath: "/Users/o<p>/cfg.json", LogDir: "/Users/op/Library/Logs/cirak/"})
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(strings.NewReader(p))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("the plist is not well-formed XML: %v\n%s", err, p)
			}
			break
		}
	}
	for _, want := range []string{"<string>/opt/a&amp;b/cirak</string>", "<string>/Users/o&lt;p&gt;/cfg.json</string>", "<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>",
		"<key>CIRAK_CONFIG</key>", "/Users/op/Library/Logs/cirak/cirak.log</string>", "/Users/op/Library/Logs/cirak/cirak.err.log</string>"} {
		if !strings.Contains(p, want) {
			t.Errorf("the plist lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "UserName") || strings.Contains(p, "TOKEN") || strings.Contains(p, "LoadCredential") {
		t.Error("an agent plist names a user, a token or a credential")
	}
	for _, o := range []plistOptions{{Label: "", Binary: "/a", ConfigPath: "/c", LogDir: "/l"}, {Label: "l", Binary: "/a\nb", ConfigPath: "/c", LogDir: "/l"}, {Label: "l", Binary: "/a", ConfigPath: "/c\x00", LogDir: "/l"}, {Label: "l", Binary: "/a", ConfigPath: "/c", LogDir: ""}} {
		if _, err := renderPlist(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	d, _ := renderPlist(plistOptions{Label: daemonLabel, Binary: "/a", ConfigPath: "/c", LogDir: "/var/log/cirak", UserName: "_cirak"})
	if !strings.Contains(d, "<key>UserName</key>\n\t<string>_cirak</string>") {
		t.Error("the daemon plist does not name the run user")
	}
}
