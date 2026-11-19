//go:build linux && !android

package route

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Gate 1 for Linux: the real factory gives a gate, and the real /proc scan
// sees a running program by its executable name and does not see an absent one.
func TestLinuxProcessNameGateScan(t *testing.T) {
	if g := newProcessNameGate([]string{"qbittorrent"}, nil); g == nil {
		t.Fatal("Linux must build a process gate")
	}
	self := filepath.Base(os.Args[0]) // e.g. route.test
	if ok, err := (&linuxProcessNameGate{names: lowerNameSet([]string{"no-such-program-zz", self}), readlink: os.Readlink}).scan(); err != nil || !ok {
		t.Fatalf("the scan must see this test process %q (ok=%v err=%v)", self, ok, err)
	}
	if ok, err := (&linuxProcessNameGate{names: lowerNameSet([]string{"no-such-program-zz"}), readlink: os.Readlink}).scan(); err != nil || ok {
		t.Fatalf("the scan must not see an absent program (ok=%v err=%v)", ok, err)
	}
}

// comm is the name at exec (cut to 15 bytes) and a program can change it;
// the rule and the searcher read /proc/<pid>/exe. A program with a long
// name (comm cut) must match by its full executable name.
func TestLinuxScanUsesTheExecutableNotComm(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	long := "zzlongbittorrentclientname" // 26 bytes: comm keeps 15
	if err := os.WriteFile(filepath.Join(dir, long), data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, long), "-test.run=^TestLinuxHelperSleep$")
	cmd.Env = append(os.Environ(), "ZZ_PROCESS_GATE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	gate := &linuxProcessNameGate{names: lowerNameSet([]string{long}), readlink: os.Readlink}
	for {
		ok, err := gate.scan()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the scan must match the full executable name (comm is cut to 15 bytes)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A link error the searcher would not also get must fail the scan (the gate
// then opens); stable errors are skipped.
func TestLinuxScanErrors(t *testing.T) {
	for _, tc := range []struct {
		err     error
		wantErr bool
	}{
		{syscall.ENOENT, false}, {syscall.ESRCH, false}, {syscall.EACCES, false}, {syscall.EPERM, false},
		{syscall.EIO, true}, {syscall.ENOMEM, true},
	} {
		g := &linuxProcessNameGate{names: lowerNameSet([]string{"zz"}), readlink: func(p string) (string, error) {
			return "", &os.PathError{Op: "readlink", Path: p, Err: tc.err}
		}}
		ok, err := g.scan()
		if ok || (err != nil) != tc.wantErr {
			t.Errorf("errno %v: ok=%v err=%v, want error=%v", tc.err, ok, err, tc.wantErr)
		}
	}
}

func TestLinuxHelperSleep(t *testing.T) {
	if os.Getenv("ZZ_PROCESS_GATE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}
