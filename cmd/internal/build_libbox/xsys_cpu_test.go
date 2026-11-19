package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// TestXSysCPUKeepsAndroidMinimalFeatures guards VilaNet fork patch #5 (go.mod):
// x/sys >= v0.42 runs MRS (getisar0) on Android when auxv is unreadable, which
// is SIGILL on some devices. The x/sys this module builds with must keep
// archInit's setMinimalFeatures() fallback, which android takes because it is
// not in the doinit() case list.
func TestXSysCPUKeepsAndroidMinimalFeatures(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-json", "golang.org/x/sys")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m golang.org/x/sys: %v", err)
	}
	var module struct {
		Version string
		Dir     string
		Replace *struct {
			Version string
			Dir     string
		}
	}
	if err := json.Unmarshal(out, &module); err != nil {
		t.Fatalf("decode go list output: %v", err)
	}
	dir, version := module.Dir, module.Version
	if module.Replace != nil {
		dir, version = module.Replace.Dir, module.Replace.Version
	}
	if dir == "" {
		t.Fatalf("golang.org/x/sys %s is not in the module cache (run go mod download)", version)
	}
	source, err := os.ReadFile(filepath.Join(dir, "cpu", "cpu_arm64.go"))
	if err != nil {
		t.Fatalf("read x/sys %s cpu_arm64.go: %v", version, err)
	}
	archInit := regexp.MustCompile(`(?s)\nfunc archInit\(\) \{(.*?)\n\}`).FindSubmatch(source)
	if archInit == nil {
		t.Fatalf("x/sys %s: archInit not found in cpu/cpu_arm64.go", version)
	}
	body := archInit[1]
	if !bytes.Contains(body, []byte("setMinimalFeatures()")) {
		t.Fatalf("x/sys %s: archInit has no setMinimalFeatures() fallback, so android/arm64 runs doinit() and can SIGILL in getisar0; keep the go.mod replace to v0.41.0", version)
	}
	if regexp.MustCompile(`case [^:]*"android"`).Match(body) {
		t.Fatalf("x/sys %s: archInit sends android to doinit()", version)
	}
}
