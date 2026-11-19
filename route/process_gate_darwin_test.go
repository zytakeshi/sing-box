//go:build darwin && !ios

package route

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Gate 1 for macOS: the real factory gives a gate (so the cost test's
// scanGate logic is what the app runs), and the real scan sees a running
// program by its executable name and does not see an absent one.
func TestDarwinProcessNameGateScan(t *testing.T) {
	if g := newProcessNameGate([]string{"Transmission"}, nil); g == nil {
		t.Fatal("macOS must build a process gate")
	}
	self := filepath.Base(os.Args[0]) // e.g. route.test
	if ok, err := (&darwinProcessNameGate{names: lowerNameSet([]string{"no-such-program-zz", self})}).scan(); err != nil || !ok {
		t.Fatalf("the scan must see this test process %q (ok=%v err=%v)", self, ok, err)
	}
	if ok, err := (&darwinProcessNameGate{names: lowerNameSet([]string{"no-such-program-zz"})}).scan(); err != nil || ok {
		t.Fatalf("the scan must not see an absent program (ok=%v err=%v)", ok, err)
	}
}

// p_comm is fixed at exec; the path (what the rule and the searcher read)
// follows a later rename of the executable. The scan must use the path: a
// program started as zzoldname and renamed to a rule name must match.
// The helper is a copy of this test binary (a copied system binary does not
// run on macOS).
func TestDarwinScanUsesTheExecutablePathNotPComm(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "zzoldname")
	if err := os.WriteFile(old, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(old, "-test.run=^TestDarwinHelperSleep$")
	cmd.Env = append(os.Environ(), "ZZ_PROCESS_GATE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)
	if err := os.Rename(old, filepath.Join(dir, "zzrealbtname")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	gate := &darwinProcessNameGate{names: lowerNameSet([]string{"zzrealbtname"})}
	for {
		ok, err := gate.scan()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the scan must match the current executable name (p_comm keeps the old one)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDarwinHelperSleep(t *testing.T) {
	if os.Getenv("ZZ_PROCESS_GATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}
