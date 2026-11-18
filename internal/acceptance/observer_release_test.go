//go:build linux

package acceptance

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR021-OBSERVER (#372, review follow-ups #386 P3-1): the file-canary scan holds its baseline directory
// open; every path that ends the scan's life must release it.

// ProdObserver.Close releases the scan's descriptor (a run that ends leaves no open directory).
func TestProdObserverCloseReleasesTheScanDescriptor(t *testing.T) {
	home, dir := scanDirs(t)
	before := openFDs(t)
	scan, err := NewFileCanaryScan(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if openFDs(t) <= before {
		t.Fatal("the scan holds no descriptor: this test would pin nothing")
	}
	obs := &ProdObserver{Scan: scan}
	if err := obs.Close(); err != nil {
		t.Fatal(err)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("ProdObserver.Close left the scan's descriptor open: %d before, %d after", before, after)
	}
	if _, ok := scan.Count(); ok {
		t.Fatal("a closed scan still observes")
	}
}

// A baseline that is refused (the directory is not empty) closes the descriptor it had opened.
func TestNewFileCanaryScanClosesItsDescriptorWhenTheBaselineIsRefused(t *testing.T) {
	home, dir := scanDirs(t)
	if err := os.WriteFile(filepath.Join(dir, "left-over"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := openFDs(t)
	for i := 0; i < 3; i++ {
		if _, err := NewFileCanaryScan(dir, home); err == nil {
			t.Fatal("a non-empty canary directory was accepted as the baseline")
		}
	}
	if after := openFDs(t); after != before {
		t.Fatalf("a refused baseline leaked descriptors: %d before, %d after", before, after)
	}
}
