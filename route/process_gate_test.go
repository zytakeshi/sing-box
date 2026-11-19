package route

import (
	"errors"
	"sync"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestProcessNameGateNames(t *testing.T) {
	nameRule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessName: []string{"qbittorrent.exe", "uTorrent.exe"}}}}
	pathRule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessPath: []string{`C:\x\y.exe`}}}}
	nested := option.Rule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalRule{RawLogicalRule: option.RawLogicalRule{Rules: []option.Rule{nameRule}}}}
	nestedPath := option.Rule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalRule{RawLogicalRule: option.RawLogicalRule{Rules: []option.Rule{pathRule}}}}

	names, ok := processNameGateNames([]option.Rule{nameRule}, nil)
	if !ok || len(names) != 2 {
		t.Fatalf("plain process_name rule: ok=%v names=%v", ok, names)
	}
	if names, ok = processNameGateNames([]option.Rule{nested}, nil); !ok || len(names) != 2 {
		t.Fatalf("nested process_name rule: ok=%v names=%v", ok, names)
	}
	if _, ok = processNameGateNames([]option.Rule{nameRule, pathRule}, nil); ok {
		t.Fatal("a process_path rule must disable the gate")
	}
	if _, ok = processNameGateNames([]option.Rule{nestedPath}, nil); ok {
		t.Fatal("a nested process_path rule must disable the gate")
	}
	dnsPkg := option.DNSRule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultDNSRule{RawDefaultDNSRule: option.RawDefaultDNSRule{PackageName: []string{"a.b"}}}}
	if _, ok = processNameGateNames([]option.Rule{nameRule}, []option.DNSRule{dnsPkg}); ok {
		t.Fatal("a DNS package_name rule must disable the gate")
	}
	if names, ok = processNameGateNames(nil, nil); !ok || len(names) != 0 {
		t.Fatalf("no process rule: ok=%v names=%v", ok, names)
	}
}

func TestLowerNameSetIsCaseInsensitiveSuperset(t *testing.T) {
	set := lowerNameSet([]string{"uTorrent.exe"})
	if _, ok := set["utorrent.exe"]; !ok {
		t.Fatal("folded name missing")
	}
}

// Codex review P1: a program that starts after a "none running" scan must be
// seen by its very first connection, not after the next interval.
func TestScanGateNeverAnswersClosedFromAnOlderScan(t *testing.T) {
	running := false
	scans := 0
	g := newScanGate(func() (bool, error) { scans++; return running, nil }, nil)
	if g.Active() {
		t.Fatal("nothing runs: gate must be closed")
	}
	running = true // qbittorrent.exe starts right after that scan
	if !g.Active() {
		t.Fatal("a listed program started after the last scan: the next call must rescan and open")
	}
	if scans != 2 {
		t.Fatalf("scans=%d, want 2", scans)
	}
	// A positive result is reused for a short time (lookups stay authoritative).
	if !g.Active() || scans != 2 {
		t.Fatalf("positive result not reused: scans=%d", scans)
	}
}

func TestScanGateFailedScanOpens(t *testing.T) {
	g := newScanGate(func() (bool, error) { return false, errTestScan }, nil)
	if !g.Active() {
		t.Fatal("a failed scan must open the gate")
	}
}

func TestScanGateSharesOneScanAcrossConcurrentCallers(t *testing.T) {
	var mu sync.Mutex
	scans := 0
	release := make(chan struct{})
	g := newScanGate(func() (bool, error) {
		mu.Lock()
		scans++
		mu.Unlock()
		<-release
		return false, nil
	}, nil)
	var wg sync.WaitGroup
	go func() { time.Sleep(50 * time.Millisecond); close(release) }()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.Active() }()
	}
	wg.Wait()
	// Callers that arrived while the first scan ran need one more scan that
	// started after them; never one scan per caller.
	if scans > 2 {
		t.Fatalf("scans=%d for 20 concurrent callers, want <= 2", scans)
	}
}

func TestSyntheticProcessNamesDisableGate(t *testing.T) {
	rule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessName: []string{":System"}}}}
	if _, ok := processNameGateNames([]option.Rule{rule}, nil); ok {
		t.Fatal(":System cannot be answered by a process snapshot")
	}
}

var errTestScan = errors.New("scan failed")
