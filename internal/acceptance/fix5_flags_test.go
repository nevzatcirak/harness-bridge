package acceptance

import (
	"os"
	"strings"
	"testing"
)

// R350-CLEANUP (#354 P3-3): the live command validates every flag before it creates the -out file, so
// an invalid configuration leaves no file behind. The command (build tag live, package main) is not
// unit-testable, so the order is pinned on its source: the -items parsing call comes before the
// evidence-file creation, as the configuration validation does.
func TestLiveCommandValidatesFlagsBeforeCreatingTheEvidenceFile(t *testing.T) {
	b, err := os.ReadFile("../../cmd/hb-claude-acceptance/main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	at := func(sub string) int { return strings.Index(s, sub) }
	create := at("acceptance.CreateEvidenceFile(")
	if create < 0 {
		t.Fatal("main.go does not create the evidence file through CreateEvidenceFile")
	}
	for _, before := range []string{"cfg.Validate()", "acceptance.ParseItems("} {
		i := at(before)
		if i < 0 {
			t.Errorf("main.go does not call %s", before)
		} else if i > create {
			t.Errorf("main.go calls %s after creating the evidence file", before)
		}
	}
}

func TestParseItems(t *testing.T) {
	good := map[string][]int{"1": {1}, "1,2,4": {1, 2, 4}, " 8 , 3 ": {8, 3}}
	for in, want := range good {
		got, err := ParseItems(in)
		if err != nil || len(got) != len(want) {
			t.Errorf("ParseItems(%q) = %v, %v", in, got, err)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("ParseItems(%q) = %v, want %v", in, got, want)
			}
		}
	}
	for _, in := range []string{"", "0", "9", "a", "1,,2", "1,x", "-1", "1;2"} {
		if got, err := ParseItems(in); err == nil {
			t.Errorf("ParseItems(%q) accepted: %v", in, got)
		}
	}
}
