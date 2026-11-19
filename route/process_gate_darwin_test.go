//go:build darwin && !ios

package route

import (
	"os"
	"path/filepath"
	"testing"
)

// Gate 1 for macOS: the real factory gives a gate (so the cost test's
// scanGate logic is what the app runs), and the real scan sees a running
// program by its name and does not see an absent one.
func TestDarwinProcessNameGateScan(t *testing.T) {
	if g := newProcessNameGate([]string{"Transmission"}, nil); g == nil {
		t.Fatal("macOS must build a process gate")
	}
	self := filepath.Base(os.Args[0]) // e.g. route.test
	present := &darwinProcessNameGate{names: []string{"no-such-program-zz", self}}
	if ok, err := present.scan(); err != nil || !ok {
		t.Fatalf("the scan must see this test process %q (ok=%v err=%v)", self, ok, err)
	}
	absent := &darwinProcessNameGate{names: []string{"no-such-program-zz"}}
	if ok, err := absent.scan(); err != nil || ok {
		t.Fatalf("the scan must not see an absent program (ok=%v err=%v)", ok, err)
	}
}

func TestDarwinTruncatedNameIsASuperset(t *testing.T) {
	d := &darwinProcessNameGate{names: []string{"transmission-daemon"}}
	if !d.matches("transmission-dae") { // p_comm keeps 16 characters
		t.Fatal("a truncated p_comm must match the full rule name")
	}
	if d.matches("transmission") || d.matches("") {
		t.Fatal("a short different name must not match")
	}
}
