//go:build live

// Command hb-claude-acceptance is the operator-run live acceptance driver of ADR-021 slice 3
// (ACCEPTANCE_S3.md items 1-8). It is built ONLY with `-tags live`: it is not part of the default
// build, the default tests or CI, and nothing in the repository imports it. Running it starts the
// real Claude CLI through the configured wrapper and spends subscription runs, so it is a separate,
// planner-scheduled task. It writes verdict lines, event types, timings and exit codes only.
//
//	go build -tags live -o ./hb-claude-acceptance ./cmd/hb-claude-acceptance      (in a directory only you can write)
//	sudo install -o root -g root -m 0755 ./hb-claude-acceptance <root-owned dir>/hb-claude-acceptance
//	sudo <root-owned dir>/hb-claude-acceptance -run-home <test user HOME> -root <throwaway root> \
//	    -run-uid <test user uid> -service-uid <Claude service user uid> -items 1,2,4,5,6,7,8 -out evidence.txt
//
// BUILD AND RUN. Install the binary into a ROOT-OWNED directory (for example /usr/local/libexec, next
// to the wrapper) and run it from there: a binary left in /tmp or in any directory another user can
// write can be swapped between the build and the privileged run. Never give the binary a file
// capability (CAP_CHOWN is root-equivalent, and a capability on the file lets any
// executor of it choose -run-home and -run-uid and race it).
//
// PRIVILEGE. The driver chowns the decoy files and the directories it creates to -run-uid, so the
// single run is made as root (sudo, as above); an unprivileged service user cannot chown to another
// user and the first decoy write fails closed. The driver never operates by path inside -run-home
// (that tree belongs to the run user): it walks from / without following symlinks and creates,
// chowns and removes through directory descriptors; a symlink anywhere in the -run-home path, or a
// swapped entry, makes it refuse. The sudo rule that lets this process start the wrapper is the
// operator's to provision. This comment does not change the privilege model.
//
// IDENTITY (ADR-021 form A). -run-uid and -run-home name an ISOLATED TEST USER: its UID and its HOME
// differ from the Claude service user's (hbclaude, whose HOME holds the real ~/.claude). -service-uid
// is that Claude service user's UID; it is required, never 0, and never equal to -run-uid. The driver
// cannot set HOME through sudo: the OPERATOR must provision the wrapper/sudo so that the child runs as
// -run-uid with HOME == -run-home (an operator step; this driver edits no host file). That is a
// precondition the driver does NOT verify; the evidence says so (precondition ... operator_precondition_not_verified).
// Running the child as the Claude service user in its real HOME is wrong: the canary would then test
// the production ~/.claude and ~/.config/anthropic.
//
// EVIDENCE FILE. -out is created with O_EXCL|O_NOFOLLOW through the same no-follow directory walk
// as the decoys: a symlink in ANY component of its path (the current directory is resolved first for
// a relative path), an existing file or a missing directory is refused; use an absolute path in a
// directory you own.
//
// The decoys must be owned by the run user, so the process needs the privilege to chown into the run
// user's HOME; run it the way the task prescribes. The driver removes only files and directories it
// created itself. Never pass --bare; the driver refuses it. The history comparison, the file-canary
// scan and the sink observation are not made by this command: item 1 reports INCOMPLETE (exit 3),
// never PASS, until they are observed.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/acceptance"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

func main() {
	var (
		wrapper    = flag.String("wrapper", acceptance.DefaultWrapper, "the wrapper path (the CLI is only ever run through it)")
		model      = flag.String("model", "sonnet", "model")
		runHome    = flag.String("run-home", "", "HOME of the isolated test user (absolute); never the Claude service user's HOME")
		root       = flag.String("root", "", "the throwaway root the CLI runs in (absolute)")
		runUID     = flag.Uint("run-uid", 0, "UID of the isolated test user the child runs as (its HOME must be -run-home)")
		serviceUID = flag.Uint("service-uid", 0, "UID of the Claude service user (required, never 0, never equal to -run-uid)")
		items      = flag.String("items", "1,2,4,5,6,7,8", "comma-separated acceptance items (1-8)")
		maxRuns    = flag.Int("max-runs", acceptance.DefaultMaxRuns, "hard run budget (at most 30)")
		maxTurns   = flag.Int("max-turns", acceptance.MaxTurnsLimit, "--max-turns per run (at most 5)")
		lagRuns    = flag.Int("lag-runs", 5, "runs for the result-to-exit lag (1-10)")
		candidate  = flag.Bool("candidate-mcp", false, "also add disableClaudeAiConnectors and ENABLE_CLAUDEAI_MCP_SERVERS=false (ADR-021 §8.3 finding)")
		out        = flag.String("out", "", "evidence file (default: stdout)")
	)
	flag.Parse()
	cfg := acceptance.Config{Wrapper: *wrapper, Model: *model, MaxRuns: *maxRuns, MaxTurns: *maxTurns, RunHome: *runHome, Root: *root,
		RunUID: uint32(*runUID), ServiceUID: uint32(*serviceUID), LagRuns: *lagRuns, Candidate: *candidate}
	// Validate before anything is created: an invalid configuration leaves no -out file behind.
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "configuration refused:", err)
		os.Exit(2)
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		p := *out
		if !filepath.IsAbs(p) {
			wd, werr := os.Getwd()
			if werr != nil {
				fmt.Fprintln(os.Stderr, "cannot resolve the evidence file path")
				os.Exit(2)
			}
			p = filepath.Join(wd, p)
		}
		// No component of the path may be a symlink and the file must not exist (the driver may
		// run as root): see acceptance.CreateEvidenceFile.
		f, err := acceptance.CreateEvidenceFile(filepath.Clean(p))
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot create the evidence file (a symlink in its path, an existing file or a missing directory is refused)")
			os.Exit(2)
		}
		defer f.Close()
		w = f
	}
	ev := acceptance.NewEvidence(w)
	d, err := acceptance.New(cfg, acceptance.ExecRunner{Wrapper: *wrapper}, &acceptance.OSDecoyFS{UID: int(*runUID), GID: int(*runUID)},
		claudecode.OSCanaryFS{}, acceptance.ProcFS{}, ev)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration refused:", err)
		os.Exit(2)
	}
	var nums []int
	for _, s := range strings.Split(*items, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 8 {
			fmt.Fprintln(os.Stderr, "items must be numbers 1-8")
			os.Exit(2)
		}
		nums = append(nums, n)
	}
	_ = ev.KV("driver", "started", time.Now().UTC().Format("2006-01-02T15:04:05Z"), "candidate_mcp", strconv.FormatBool(*candidate))
	verdicts := d.RunItems(context.Background(), nums)
	code := 0
	for _, v := range verdicts {
		switch v.Result {
		case acceptance.Fail:
			code = 1
		case acceptance.Incomplete:
			if code == 0 {
				code = 3
			}
		}
	}
	if code != 0 {
		if f, ok := w.(*os.File); ok && f != os.Stdout {
			_ = f.Close() // os.Exit skips the defers
		}
		os.Exit(code)
	}
}
