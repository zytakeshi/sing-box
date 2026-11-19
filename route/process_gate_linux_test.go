//go:build linux && !android

package route

import (
	"os"
	"path/filepath"
	"testing"
)

// Gate 1 for Linux: the real factory gives a gate, and the real /proc scan
// sees a running program by its comm and does not see an absent one.
func TestLinuxProcessNameGateScan(t *testing.T) {
	if g := newProcessNameGate([]string{"qbittorrent"}, nil); g == nil {
		t.Fatal("Linux must build a process gate")
	}
	self := filepath.Base(os.Args[0]) // e.g. route.test
	if len(self) > linuxCommLen {
		self = self[:linuxCommLen]
	}
	present := &linuxProcessNameGate{names: []string{"no-such-program-zz", self}}
	if ok, err := present.scan(); err != nil || !ok {
		t.Fatalf("the scan must see this test process %q (ok=%v err=%v)", self, ok, err)
	}
	absent := &linuxProcessNameGate{names: []string{"no-such-program-zz"}}
	if ok, err := absent.scan(); err != nil || ok {
		t.Fatalf("the scan must not see an absent program (ok=%v err=%v)", ok, err)
	}
}

func TestLinuxTruncatedCommIsASuperset(t *testing.T) {
	g := &linuxProcessNameGate{names: []string{"transmission-daemon"}}
	if !g.matches("transmission-da") { // comm keeps 15 characters
		t.Fatal("a truncated comm must match the full rule name")
	}
	if g.matches("transmission") || g.matches("") {
		t.Fatal("a short different name must not match")
	}
}
