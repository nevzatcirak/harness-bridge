package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// R547-FIX-1 (#552): --claude-home validation (P2-1), the doctor --smoke remote guard (P2-2), the staged-flow
// symlink refusal (P2-3, P3-1), the AST pins over the S3 files (P3-2) and the systemctl argv (P3-6).

// ---- P2-1: --claude-home is validated before anything is written, staged or printed ----

func TestInstallServiceClaudeHomeIsValidatedBeforeAnythingIsWritten(t *testing.T) {
	bad := []string{"/", "-/", "+/etc", "!/x", "@/x", ":/x", "relative/dir", "../..", "/home/../etc", "/home/x/", "/home//x", "/home/./x",
		"/etc", "/usr", "/var", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/proc", "/sys", "/dev", "/run", "/root"}
	for _, staged := range []bool{false, true} {
		for i, v := range bad {
			// the subtest name becomes part of the temporary directory path, and a value such as "!/x" would
			// make that path unsafe for a unit file: name the case by its index
			name := "case-" + strconv.Itoa(i)
			if staged {
				name = "staged-" + name
			}
			t.Run(name, func(t *testing.T) {
				if staged {
					defer rootOwnerForTest(t)()
				}
				e := newSvcEnv(t)
				h := e.h
				if staged {
					h.svcUID = os.Geteuid() + 4242
				}
				args := e.args("--claude-home", v) // builds the workspace directory the arguments name
				before := listTree(t, h.base)
				code := h.init(args...)
				if code != 2 {
					t.Fatalf("exit %d (want the usage exit 2)\n%s", code, h.all())
				}
				if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") || h.stage != "" {
					t.Error("something was written or staged")
				}
				if strings.Contains(h.out.String(), "install -") || strings.Contains(h.out.String(), "ReadWritePaths") || len(h.runs) != 0 {
					t.Errorf("something was printed or run (%q):\n%s", v, h.out.String())
				}
				if !strings.Contains(h.errb.String(), "--claude-home") {
					t.Errorf("the refusal does not name the flag:\n%s", h.errb.String())
				}
				// a leading - + ! @ : is refused for what it means to systemd, not only for being relative
				if strings.ContainsRune("-+!@:", rune(v[0])) && !strings.Contains(h.errb.String(), "prefix of its own") {
					t.Errorf("%q: the refusal does not explain the systemd prefix:\n%s", v, h.errb.String())
				}
			})
		}
	}
	// a valid home is accepted and is the only ReadWritePaths= entry
	for _, v := range []string{"/home/hbclaude", "/srv/claude", "/var/lib/claude", "/opt/claude-home"} {
		e := newSvcEnv(t)
		if code := e.h.init(e.args("--claude-home", v)...); code != 0 {
			t.Fatalf("%s: exit %d\n%s", v, code, e.h.all())
		}
		unit := directives(readF(t, filepath.Join(e.unitDir, "cirak.service")))
		if !strings.Contains(unit, "ReadWritePaths="+v+"\n") && !strings.HasSuffix(unit, "ReadWritePaths="+v) {
			t.Errorf("%s: no ReadWritePaths entry:\n%s", v, unit)
		}
	}
}

// ---- P2-2: doctor --smoke never calls the service in remote mode ----

func TestDoctorSmokeIsUnverifiedInRemoteModeAndNeverCallsTheService(t *testing.T) {
	h := newDH(t)
	cert, key := filepath.Join(h.f.dir, "c.pem"), filepath.Join(h.f.dir, "k.pem")
	writeFile(t, cert, string(selfSignedPEM(t, time.Now().Add(24*30*time.Hour))), 0o644)
	writeFile(t, key, "KEY-CONTENT-CANARY\n", 0o600)
	h.env["CIRAK_TLS_CERT"], h.env["CIRAK_TLS_KEY"] = cert, key
	h.env["CIRAK_PUBLIC_ORIGIN"], h.env["CIRAK_PRINCIPAL"] = "https://bridge.example", "operator"
	h.env["CIRAK_LISTEN"] = "127.0.0.1:9443"
	h.f.write(t, `{"token":{"file":"@TOKEN@","expires":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	h.smokeRes = []string{"never"}
	code := h.run("--config", h.f.cfg, "--smoke")
	if !strings.Contains(h.out.String(), "UNVERIFIED smoke: only the local endpoint is exercised; this is remote mode") {
		t.Fatalf("exit %d, no remote-mode UNVERIFIED line:\n%s", code, h.out.String())
	}
	if h.smokeURL != "" || h.smokeTok != "" {
		t.Errorf("the smoke function was called in remote mode (url %q, token sent %v)", h.smokeURL, h.smokeTok != "")
	}
	if code == 0 {
		t.Error("an UNVERIFIED smoke must not exit 0")
	}
}

// ---- P2-3 and P3-1: a symlinked unit directory is refused in the pre-flight, in both flows ----

func TestInstallServiceRefusesASymlinkedUnitDirectoryBeforeAnythingIsPrintedOrStaged(t *testing.T) {
	setup := func(t *testing.T, staged bool) (*svcEnv, string) {
		t.Helper()
		e := newSvcEnv(t)
		real := e.h.dir("real-systemd", 0o755)
		link := filepath.Join(e.h.base, "systemd-link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		systemUnitDir = link
		if staged {
			e.h.svcUID = os.Geteuid() + 4242
		}
		return e, real
	}
	for _, staged := range []bool{true, false} {
		name := "same-user flow"
		if staged {
			name = "staged flow"
		}
		t.Run(name, func(t *testing.T) {
			if staged {
				defer rootOwnerForTest(t)()
			}
			e, real := setup(t, staged)
			h := e.h
			args := e.args()
			before := listTree(t, h.base)
			code := h.init(args...)
			if code != 1 || !strings.Contains(h.errb.String(), "symbolic link") || !strings.Contains(h.errb.String(), "no service file was written or staged") {
				t.Fatalf("exit %d:\n%s", code, h.all())
			}
			if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
				t.Errorf("the tree changed:\n%v\n%v", before, listTree(t, h.base))
			}
			if h.stage != "" {
				t.Error("a staging directory was made (the token would sit in it)")
			}
			if strings.Contains(h.out.String(), "install -") || strings.Contains(h.out.String(), "rm -r") {
				t.Errorf("a command was printed before the refusal:\n%s", h.out.String())
			}
			if entries, _ := os.ReadDir(real); len(entries) != 0 {
				t.Errorf("something was written through the symlink: %v", entries)
			}
		})
	}
}

// A staging directory that holds the token never outlives a failed staged run.
func TestInstallServiceAFailedStagedRunRemovesItsStagingDirectory(t *testing.T) {
	defer rootOwnerForTest(t)()
	e := newSvcEnv(t)
	h := e.h
	h.svcUID = os.Geteuid() + 4242
	// the unit's staging name is taken, so staging the unit fails after the config and the token were staged
	h.stage = h.dir("stage", 0o700)
	writeFile(t, filepath.Join(h.stage, "cirak.service"), "TAKEN", 0o600)
	code := h.init(e.args()...)
	if code == 0 {
		t.Fatalf("the run was expected to fail:\n%s", h.all())
	}
	if exists(h.stage) {
		entries, _ := os.ReadDir(h.stage)
		t.Errorf("the staging directory survived a failed run: %v", entries)
	}
}

// The staged flow says "staged", never "placed", for a file that was only staged.
func TestInstallServiceStagedFlowSaysStagedNotPlaced(t *testing.T) {
	defer rootOwnerForTest(t)()
	e := newSvcEnv(t)
	h := e.h
	h.svcUID = os.Geteuid() + 4242
	if code := h.init(e.args()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if strings.Contains(h.out.String(), "Service file placed") || !strings.Contains(h.out.String(), "Service file staged (not yet installed): ") {
		t.Errorf("the staged flow does not say staged:\n%s", h.out.String())
	}
	// the same-user flow that writes the file does say placed
	e = newSvcEnv(t)
	if code := e.h.init(e.args()...); code != 0 || !strings.Contains(e.h.out.String(), "Service file placed: ") {
		t.Errorf("exit %d, the same-user flow does not say placed:\n%s", code, e.h.out.String())
	}
}

// ---- P3-2: no ownership, mode or process calls in the S3 files; the single allowed doctor command ----

func TestServiceAndDoctorCodeHaveNoOwnershipModeOrProcessCalls(t *testing.T) {
	bannedSel := map[string]bool{"os.StartProcess": true, "syscall.Exec": true, "syscall.ForkExec": true, "syscall.StartProcess": true,
		"os.Chown": true, "os.Lchown": true, "os.Chmod": true, "unix.Exec": true}
	for _, file := range []string{"initservice.go", "unit.go", "doctor.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				if id, ok := sel.X.(*ast.Ident); ok && bannedSel[id.Name+"."+name] {
					t.Errorf("%s: %s uses %s.%s", file, fd.Name.Name, id.Name, name)
				}
				switch {
				case strings.HasPrefix(name, "Chown") || strings.HasPrefix(name, "Lchown") || strings.HasPrefix(name, "Fchown"),
					strings.HasPrefix(name, "Chmod") || strings.HasPrefix(name, "Fchmod") || strings.HasPrefix(name, "Lchmod"):
					t.Errorf("%s: %s uses %s (the service code never changes an owner or a mode)", file, fd.Name.Name, name)
				case name == "Command" || name == "CommandContext":
					if file != "doctor.go" || fd.Name.Name != "prodShowUnit" || name != "CommandContext" {
						t.Errorf("%s: %s starts a process with %s; only doctor's prodShowUnit may (systemctl show)", file, fd.Name.Name, name)
					}
				}
				return true
			})
		}
	}
}

// ---- P3-6: the argv of the doctor's one command, including the -- terminator ----

func TestDoctorShowUnitArgvIsReadOnlyAndEndsOptionsBeforeTheName(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "doctor.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]ast.Expr
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "prodShowUnit" {
			ast.Inspect(fd, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CommandContext" {
						calls = append(calls, c.Args)
					}
				}
				return true
			})
		}
	}
	if len(calls) != 1 {
		t.Fatalf("prodShowUnit has %d CommandContext calls, want 1", len(calls))
	}
	want := []string{"systemctl", "show", "-p", "Environment", "-p", "EnvironmentFiles", "-p", "UnsetEnvironment", "--"}
	args := calls[0][1:] // the first argument is the context
	if len(args) != len(want)+1 {
		t.Fatalf("argv has %d entries, want %d (the %d literals and the unit name)", len(args), len(want)+1, len(want))
	}
	for i, w := range want {
		lit, ok := args[i].(*ast.BasicLit)
		if !ok {
			t.Fatalf("argument %d is not a literal", i)
		}
		if got, err := strconv.Unquote(lit.Value); err != nil || got != w {
			t.Errorf("argument %d is %s, want %q", i, lit.Value, w)
		}
	}
	if id, ok := args[len(want)].(*ast.Ident); !ok || id.Name != "name" {
		t.Errorf("the last argument must be the validated unit name, after --")
	}
}

// The warning about a unit that shadows the packaged one says that the drop-in applies to it (#548 P3-1, #552 P3-3).
func TestInstallServiceShadowWarningSaysTheDropInApplies(t *testing.T) {
	e := newSvcEnv(t)
	e.withPackagedUnit(t)
	writeFile(t, filepath.Join(e.unitDir, "cirak.service"), "[Service]\nExecStart=/bin/true\n", 0o644)
	if code := e.h.init(e.args("--force")...); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.h.all())
	}
	if !strings.Contains(e.h.out.String(), "this drop-in applies to it") {
		t.Errorf("the shadow warning does not say the drop-in applies:\n%s", e.h.out.String())
	}
}
