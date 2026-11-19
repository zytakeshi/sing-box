package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestXSysCPUKeepsAndroidMinimalFeatures guards VilaNet fork patch #5 (go.mod):
// x/sys >= v0.42 runs MRS (getisar0) on Android when auxv is unreadable, which
// is SIGILL on some devices. It builds a small android/arm64 program with the
// x/sys this module resolves and fails when the linker keeps
// golang.org/x/sys/cpu.getisar0, i.e. when android can reach the MRS read.
func TestXSysCPUKeepsAndroidMinimalFeatures(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "xsyscpu")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, "./testdata/xsyscpu")
	build.Env = append(os.Environ(), "GOWORK=off", "GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build android/arm64 x/sys/cpu probe: %v\n%s", err, out)
	}
	nm := exec.Command("go", "tool", "nm", binary)
	nm.Env = append(os.Environ(), "GOWORK=off")
	symbols, err := nm.CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm: %v\n%s", err, symbols)
	}
	if !strings.Contains(string(symbols), "golang.org/x/sys/cpu.") {
		t.Fatalf("probe links no golang.org/x/sys/cpu symbol; the check proves nothing")
	}
	for _, name := range []string{"golang.org/x/sys/cpu.getisar0", "golang.org/x/sys/cpu.readARM64Registers"} {
		if strings.Contains(string(symbols), name) {
			t.Fatalf("android/arm64 links %s: x/sys/cpu can read ID_AA64ISAR0_EL1 with MRS (SIGILL on some devices); keep the go.mod replace to x/sys v0.41.0", name)
		}
	}
}
