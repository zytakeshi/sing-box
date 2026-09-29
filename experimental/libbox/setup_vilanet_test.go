package libbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sagernet/sing/service"
)

// VilaNet fork patch 4: DisableCrashReport must leave no crash log, platform
// snapshot or configuration snapshot behind.
func TestSetupDisableCrashReportWritesNothing(t *testing.T) {
	base := t.TempDir()
	working := filepath.Join(base, "working")
	options := &SetupOptions{
		BasePath:           base,
		WorkingPath:        working,
		TempPath:           filepath.Join(base, "temp"),
		CrashReportSource:  "test",
		PlatformMetadata:   `{"k":"v"}`,
		DisableCrashReport: true,
	}
	t.Cleanup(func() { sCrashReportDisabled = false })
	if err := Setup(options); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	saveConfigSnapshot(`{"outbounds":[{"password":"secret"}]}`)
	for _, path := range []string{
		filepath.Join(working, "CrashReport-test.log"),
		platformSnapshotPath(),
		configSnapshotPath(),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists (err=%v), want absent", path, err)
		}
	}
}

func TestSetupDefaultStillRedirectsStderr(t *testing.T) {
	base := t.TempDir()
	working := filepath.Join(base, "working")
	if err := Setup(&SetupOptions{
		BasePath:          base,
		WorkingPath:       working,
		TempPath:          filepath.Join(base, "temp"),
		CrashReportSource: "test",
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(working, "CrashReport-test.log")); err != nil {
		t.Fatalf("crash log missing on default path: %v", err)
	}
}

// VilaNet fork patch 4 (extension): with DisableCrashReport the OOM report
// recorder is never created, so no draft directory (core log, connections,
// pprof, heap dump) can appear under the working path.
func TestOOMRecorderGatedByDisableCrashReport(t *testing.T) {
	base := t.TempDir()
	working := filepath.Join(base, "working")
	t.Cleanup(func() { sCrashReportDisabled = false })
	for _, disabled := range []bool{true, false} {
		if err := Setup(&SetupOptions{
			BasePath:           base,
			WorkingPath:        working,
			TempPath:           filepath.Join(base, "temp"),
			CrashReportSource:  "test",
			DisableCrashReport: disabled,
		}); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		recorder := newOOMRecorder(service.ContextWithDefaultRegistry(context.Background()), nil)
		if disabled && recorder != nil {
			t.Fatal("OOM recorder created although DisableCrashReport is set")
		}
		if !disabled {
			if recorder == nil {
				t.Fatal("OOM recorder missing on the default path")
			}
			_ = recorder.Close()
		}
	}
}
