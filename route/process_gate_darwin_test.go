//go:build darwin && !ios

package route

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func darwinTestGate(names ...string) *darwinProcessNameGate {
	return &darwinProcessNameGate{names: lowerNameSet(names), path: darwinProcessPath}
}

func uniqueName(t *testing.T, prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(b)
}

// Gate 1 for macOS: the real factory gives a gate (so the cost test's
// scanGate logic is what the app runs), and the real scan sees a running
// program by its executable name and does not see an absent one.
func TestDarwinProcessNameGateScan(t *testing.T) {
	if g := newProcessNameGate([]string{"Transmission"}, nil); g == nil {
		t.Fatal("macOS must build a process gate")
	}
	self := filepath.Base(os.Args[0]) // e.g. route.test
	if ok, err := darwinTestGate("no-such-program-zz", self).scan(); err != nil || !ok {
		t.Fatalf("the scan must see this test process %q (ok=%v err=%v)", self, ok, err)
	}
	if ok, err := darwinTestGate("no-such-program-zz").scan(); err != nil || ok {
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
	old := filepath.Join(dir, uniqueName(t, "zzold"))
	if err := os.WriteFile(old, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(old, "-test.run=^TestDarwinHelperSleep$")
	cmd.Env = append(os.Environ(), "ZZ_PROCESS_GATE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill helper: %v", err)
		}
		_ = cmd.Wait() // "signal: killed" is the expected result
	}()
	time.Sleep(300 * time.Millisecond)
	renamed := filepath.Join(dir, uniqueName(t, "zzbt"))
	if err := os.Rename(old, renamed); err != nil {
		t.Fatal(err)
	}
	gate := darwinTestGate(filepath.Base(renamed))
	buf := make([]byte, darwinProcPathSize)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, err := darwinProcessPath(int32(cmd.Process.Pid), buf); err == nil && filepath.Base(p) == filepath.Base(renamed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper's path never showed the new name")
		}
		time.Sleep(50 * time.Millisecond)
	}
	ok, err := gate.scan()
	if err != nil || !ok {
		t.Fatalf("the scan must match the current executable name (p_comm keeps the old one): ok=%v err=%v", ok, err)
	}
}

// A path error the searcher would not also get must fail the scan (the gate
// then opens); stable errors are skipped.
func TestDarwinScanErrors(t *testing.T) {
	for _, tc := range []struct {
		err     error
		wantErr bool
	}{
		{syscall.ESRCH, false}, {syscall.ENOENT, false}, {syscall.EPERM, false}, {syscall.EACCES, false},
		{syscall.EIO, true}, {syscall.ENOMEM, true}, {syscall.EINVAL, true},
	} {
		g := &darwinProcessNameGate{names: lowerNameSet([]string{"zz"}), path: func(int32, []byte) (string, error) { return "", tc.err }}
		ok, err := g.scan()
		if ok || (err != nil) != tc.wantErr {
			t.Errorf("errno %v: ok=%v err=%v, want error=%v", tc.err, ok, err, tc.wantErr)
		}
	}
}

func TestDarwinHelperSleep(t *testing.T) {
	if os.Getenv("ZZ_PROCESS_GATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}
