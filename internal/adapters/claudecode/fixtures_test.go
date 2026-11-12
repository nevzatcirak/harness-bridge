package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	spikeSession = "4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	spikeRoot    = "/opt/developer/hb-work/a/p6-spike/proj-x"
)

func spikeCfg(uuid string) Config {
	return Config{RequestedUUID: uuid, CanonicalRoot: spikeRoot, AllowedVersions: []string{"2.1.322"}, Tools: []string{"Read", "Glob", "Grep"}}
}

// replay decodes a recorded stream with the real decoder and feeds it, then EOF
// and the exit status the evidence recorded.
func replay(t *testing.T, name string, cfg Config, pre []Step, exitStep Step) Verdict {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "spike", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := NewDecoder(f, Limits{})
	m := NewMachine(cfg)
	for _, s := range pre {
		m.Feed(s)
	}
	for {
		e, err := d.Next()
		if err != nil {
			break
		}
		m.Feed(Step{Kind: StepLine, Event: e})
	}
	m.Feed(eof())
	m.Feed(exitStep)
	return m.Verdict()
}

// Recorded OBSERVED shapes map to the OBSERVED verdicts.
func TestSpikeFixtureReplay(t *testing.T) {
	t.Run("step 2 resume ok, exit 0 -> completed", func(t *testing.T) {
		assertVerdict(t, replay(t, "step2_resume_ok.ndjson", spikeCfg(spikeSession), nil, exit(0)), "3", "completed", "", true)
	})
	t.Run("step 3 error_max_turns, exit 1 -> failed", func(t *testing.T) {
		v := replay(t, "step3_max_turns.ndjson", spikeCfg(spikeSession), nil, exit(1))
		assertVerdict(t, v, "4", "failed", "", true)
		if v.Code != "ERROR_MAX_TURNS" {
			t.Fatalf("code %q", v.Code)
		}
	})
	t.Run("step 4 unknown id, exit 1 -> failed NOT_FOUND", func(t *testing.T) {
		v := replay(t, "step4_unknown_id.ndjson", spikeCfg("00000000-0000-4000-8000-000000000000"), nil, exit(1))
		assertVerdict(t, v, "2", "failed", "", true)
		if v.Code != "NOT_FOUND" {
			t.Fatalf("code %q", v.Code)
		}
	})
	t.Run("step 6 SIGINT acknowledged, exit 0 -> interrupted", func(t *testing.T) {
		assertVerdict(t, replay(t, "step6_sigint.ndjson", spikeCfg(spikeSession), []Step{userInt}, exit(0)), "5", "interrupted", "", true)
	})
	t.Run("step 6 SIGTERM, exit 143, no result -> unknown", func(t *testing.T) {
		assertVerdict(t, replay(t, "step6_sigterm.ndjson", spikeCfg(spikeSession), nil, exit(143)), "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false)
		assertVerdict(t, replay(t, "step6_sigterm.ndjson", spikeCfg(spikeSession), []Step{userInt}, exit(143)), "6", "unknown", "ABORT_UNCONFIRMED", false)
	})
	t.Run("step 7 denied Write is a success result with a denial list", func(t *testing.T) {
		v := replay(t, "step7_denial.ndjson", spikeCfg(spikeSession), nil, exit(0))
		assertVerdict(t, v, "3", "completed", "", true)
		if len(v.PermissionDenials) != 1 || v.PermissionDenials[0].ToolName != "Write" {
			t.Fatalf("denials %+v", v.PermissionDenials)
		}
	})
	t.Run("the same fixtures under a different root are an identity mismatch", func(t *testing.T) {
		cfg := spikeCfg(spikeSession)
		cfg.CanonicalRoot = "/opt/developer/hb-work/a/p6-spike/proj-y"
		assertVerdict(t, replay(t, "step2_resume_ok.ndjson", cfg, nil, exit(0)), "0", "unknown", "IDENTITY_MISMATCH", false)
	})
}
