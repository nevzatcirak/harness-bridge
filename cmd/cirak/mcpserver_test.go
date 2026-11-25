package main

import (
	"testing"

	"github.com/nevzatcirak/cirak/internal/claudewire"
)

// #276 P3-1: a nil adapter must leave Server.Claude a nil INTERFACE (not an interface holding a
// nil pointer), and a running adapter must be the very pointer that was passed.
func TestNewMCPServerSetsClaudeOnlyWhenTheAdapterRuns(t *testing.T) {
	var none *claudewire.Harness
	if s := newMCPServer(nil, nil, "t", nil, nil, false, none); s.Claude != nil {
		t.Fatalf("a nil adapter produced a non-nil interface: %#v", s.Claude)
	}
	h := &claudewire.Harness{}
	s := newMCPServer(nil, nil, "t", nil, nil, true, h)
	got, ok := s.Claude.(*claudewire.Harness)
	if !ok || got != h {
		t.Fatalf("the running adapter was not set as given: %#v", s.Claude)
	}
	if !s.ReadOnly || s.Token != "t" {
		t.Fatalf("the other fields were not carried: %+v", s)
	}
}
