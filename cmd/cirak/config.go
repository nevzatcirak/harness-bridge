package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nevzatcirak/cirak/internal/config"
)

// ADR-027 section 1.2: flag, then environment, then the config file, then the default. This file wires the
// internal/config package to the process: the flags of the daemon, the discovery of the file, and the one
// function (loadEffective) that main and `cirak config check` share, so a configuration that checks clean is
// the one that starts.

// systemConfigPath is a variable only so that tests can point discovery at a temp directory.
var systemConfigPath = "/etc/cirak/config.json"

// serveFlags are the flags of the daemon. Only settings with a real operational need have one; every other
// value comes from the environment or the file.
type serveFlags struct {
	config    string
	listen    string
	readOnly  bool
	tokenFile string
}

const precedenceHelp = `Precedence per setting: flag, then environment (CIRAK_*, plus the legacy names of the compatibility
window), then the config file, then the default. The config file is --config, else $CIRAK_CONFIG, else the first
existing of /etc/cirak/config.json and $XDG_CONFIG_HOME/cirak/config.json (~/.config/cirak/config.json); no file
in the current directory is ever read. With no file the daemon reads the environment exactly as before.`

func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *serveFlags) {
	f := &serveFlags{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.config, "config", "", "path of the config file (it must exist)")
	fs.StringVar(&f.listen, "listen", "", "listen address (server.listen; loopback unless remote mode)")
	fs.BoolVar(&f.readOnly, "read-only", false, "read-only ingress (server.read_only)")
	fs.StringVar(&f.tokenFile, "token-file", "", "absolute path of the token file (token.file); never the token itself")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s [serve] [--config path] [--listen addr] [--read-only] [--token-file path]\n\n%s\n", name, precedenceHelp)
		fs.PrintDefaults()
	}
	return fs, f
}

// parseServeFlags parses the daemon arguments. The word "serve" is accepted and optional (a unit may spell
// it); any other positional argument is a usage error.
func parseServeFlags(args []string, stderr io.Writer) (serveFlags, error) {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs, f := newFlagSet("cirak", stderr)
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return serveFlags{}, errors.New("unexpected argument " + fs.Arg(0))
	}
	return *f, nil
}

// asMap is the flags as key paths in the environment form; only a flag that was given is present.
func (f serveFlags) asMap() map[string]string {
	m := map[string]string{}
	if f.listen != "" {
		m["server.listen"] = f.listen
	}
	if f.readOnly {
		m["server.read_only"] = "1"
	}
	if f.tokenFile != "" {
		m["token.file"] = f.tokenFile
	}
	return m
}

// locateConfig returns the config file in use and whether it was asked for. An asked-for file (--config,
// then CIRAK_CONFIG) must exist; otherwise the first existing of the system file and the per-user file wins;
// none means environment only. No relative path and no file in the current directory is ever considered.
func locateConfig(flagPath string, env func(string) string, home func() (string, error)) (path string, explicit bool, err error) {
	switch {
	case flagPath != "":
		path, explicit = flagPath, true
	case env(config.EnvConfig) != "":
		path, explicit = env(config.EnvConfig), true
	}
	if explicit {
		if !filepath.IsAbs(path) {
			return "", true, fmt.Errorf("the config path %q must be absolute", path)
		}
		ok, err := config.Exists(path)
		if err != nil {
			return "", true, err
		}
		if !ok {
			return "", true, fmt.Errorf("config file %s does not exist", path)
		}
		return path, true, nil
	}
	candidates := []string{systemConfigPath}
	if x := env("XDG_CONFIG_HOME"); x != "" {
		candidates = append(candidates, filepath.Join(x, "cirak", "config.json"))
	} else if h, herr := home(); herr == nil && h != "" {
		candidates = append(candidates, filepath.Join(h, ".config", "cirak", "config.json"))
	}
	for _, c := range candidates {
		if !filepath.IsAbs(c) {
			continue
		}
		ok, err := config.Exists(c)
		if err != nil {
			return "", false, err
		}
		if ok {
			return c, false, nil
		}
	}
	return "", false, nil
}

// loaded is the effective configuration of one start or one check.
type loaded struct {
	res      *config.Result
	path     string   // the config file in use, "" for environment only
	warnings []string // value-free
}

// loadEffective discovers and loads the config file, then resolves the precedence. env is the environment
// after the ADR-026 rule (cirakEnv). Nothing is started and no secret is read.
func loadEffective(f serveFlags, env func(string) string, uid uint32, home func() (string, error)) (*loaded, error) {
	path, _, err := locateConfig(f.config, env, home)
	if err != nil {
		return nil, err
	}
	var file *config.File
	if path != "" {
		if file, err = config.Load(path, uid); err != nil {
			return nil, err
		}
	}
	res, err := config.Resolve(config.Input{Env: env, File: file, Flags: f.asMap()})
	if err != nil {
		return nil, err
	}
	l := &loaded{res: res, path: path, warnings: append([]string(nil), res.Warnings...)}
	if path != "" && env("INVOCATION_ID") != "" && !isSystemPath(path) {
		l.warnings = append(l.warnings, "config file "+path+" is a per-user file used by a service run; a service should use "+systemConfigPath+" (ADR-027 section 5)")
	}
	return l, nil
}

func isSystemPath(p string) bool { return p == systemConfigPath }

// describePath is the startup log line: the path in use, or the absence of a file. Never a value.
func (l *loaded) describePath() string {
	if l.path == "" {
		return "config: no config file (environment only)"
	}
	return "config: using " + l.path
}

var nowFunc = time.Now

func processHome() (string, error) { return os.UserHomeDir() }
