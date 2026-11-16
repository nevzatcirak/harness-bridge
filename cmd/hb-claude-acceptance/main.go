//go:build live

// Command hb-claude-acceptance is the operator-run live acceptance driver of ADR-021 slice 3
// (ACCEPTANCE_S3.md items 1-8). It is built ONLY with `-tags live`: it is not part of the default
// build, the default tests or CI, and nothing in the repository imports it. Running it starts the
// real Claude CLI through the configured wrapper and spends subscription runs, so it is a separate,
// planner-scheduled task. It writes verdict lines, event types, timings and exit codes only.
//
//	go build -tags live -o /tmp/hb-claude-acceptance ./cmd/hb-claude-acceptance
//	sudo -u harness-bridge /tmp/hb-claude-acceptance -run-home /home/hbclaude -root /opt/harness-bridge-test/claude-ws \
//	    -run-uid <hbclaude uid> -service-uid <harness-bridge uid> -items 1,2,4,5,6,7,8 -out evidence.txt
//
// The decoys must be owned by the run user, so the process needs the privilege to chown into the run
// user's HOME; run it the way the task prescribes. Never pass --bare; the driver refuses it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
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
		runHome    = flag.String("run-home", "", "HOME of the run user (absolute)")
		root       = flag.String("root", "", "the throwaway root the CLI runs in (absolute)")
		runUID     = flag.Uint("run-uid", 0, "UID of the run user")
		serviceUID = flag.Uint("service-uid", 0, "UID of the Claude service user")
		items      = flag.String("items", "1,2,4,5,6,7,8", "comma-separated acceptance items (1-8)")
		maxRuns    = flag.Int("max-runs", acceptance.DefaultMaxRuns, "hard run budget (at most 30)")
		maxTurns   = flag.Int("max-turns", acceptance.MaxTurnsLimit, "--max-turns per run (at most 5)")
		lagRuns    = flag.Int("lag-runs", 5, "runs for the result-to-exit lag (1-10)")
		candidate  = flag.Bool("candidate-mcp", false, "also add disableClaudeAiConnectors and ENABLE_CLAUDEAI_MCP_SERVERS=false (ADR-021 §8.3 finding)")
		out        = flag.String("out", "", "evidence file (default: stdout)")
	)
	flag.Parse()
	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot create the evidence file")
			os.Exit(2)
		}
		defer f.Close()
		w = f
	}
	cfg := acceptance.Config{Wrapper: *wrapper, Model: *model, MaxRuns: *maxRuns, MaxTurns: *maxTurns, RunHome: *runHome, Root: *root,
		RunUID: uint32(*runUID), ServiceUID: uint32(*serviceUID), LagRuns: *lagRuns, Candidate: *candidate}
	ev := acceptance.NewEvidence(w)
	d, err := acceptance.New(cfg, acceptance.ExecRunner{Wrapper: *wrapper}, acceptance.OSDecoyFS{UID: int(*runUID), GID: int(*runUID)},
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
	for _, v := range verdicts {
		if v.Result == acceptance.Fail {
			os.Exit(1)
		}
	}
}
