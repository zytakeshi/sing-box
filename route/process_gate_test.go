package route

import (
	"errors"
	"runtime"
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
	defer setNegativeTTL(0)()
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

func TestScanGateSharesScansAndNeverRunsTwoAtOnce(t *testing.T) {
	defer setNegativeTTL(0)()
	var mu sync.Mutex
	scans, inFlight, maxInFlight := 0, 0, 0
	release := make(chan struct{})
	started := make(chan struct{}, 64)
	g := newScanGate(func() (bool, error) {
		mu.Lock()
		scans++
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return false, nil
	}, nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); g.Active() }()
	<-started // scan 1 runs; the next 20 callers arrive after it started
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.Active() }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("max concurrent scans = %d, want 1", maxInFlight)
	}
	// Scan 1 started before the 20 callers, so they need at least one more;
	// callers that arrive while scan 2 runs need scan 3. Never one per caller.
	if scans < 2 || scans > 3 {
		t.Fatalf("scans=%d for 1+20 callers, want 2..3", scans)
	}
}

// Codex review round 2 P1: two calls can read the same clock tick. A negative
// scan that started in the same tick as a later call must not answer for it.
func TestScanGateEqualClockTickStillRescans(t *testing.T) {
	defer setNegativeTTL(0)()
	tick := time.Now()
	running := false
	scans := 0
	g := newScanGate(func() (bool, error) { scans++; return running, nil }, nil)
	g.now = func() time.Time { return tick }
	if g.Active() {
		t.Fatal("unexpected positive")
	}
	running = true
	if !g.Active() {
		t.Fatal("reused an older negative scan read at the same clock tick")
	}
	if scans != 2 {
		t.Fatalf("scans=%d, want 2", scans)
	}
}

func TestSyntheticProcessNamesDisableGate(t *testing.T) {
	rule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessName: []string{":System"}}}}
	if _, ok := processNameGateNames([]option.Rule{rule}, nil); ok {
		t.Fatal(":System cannot be answered by a process snapshot")
	}
}

var errTestScan = errors.New("scan failed")

func setNegativeTTL(d time.Duration) func() {
	old := processNameGateNegativeTTL
	processNameGateNegativeTTL = d
	return func() { processNameGateNegativeTTL = old }
}

// The shipped setting: a negative scan answers for 1 s (measured trade-off),
// and a rescan after that sees a program that started meanwhile.
func TestScanGateNegativeTTLBoundsTheWindow(t *testing.T) {
	defer setNegativeTTL(time.Second)()
	now := time.Now()
	running := false
	scans := 0
	g := newScanGate(func() (bool, error) { scans++; return running, nil }, nil)
	g.now = func() time.Time { return now }
	if g.Active() {
		t.Fatal("unexpected positive")
	}
	running = true
	if g.Active() || scans != 1 {
		t.Fatalf("inside the 1 s window the negative scan is reused (scans=%d)", scans)
	}
	now = now.Add(1001 * time.Millisecond)
	if !g.Active() || scans != 2 {
		t.Fatalf("after the window a rescan must see the program (scans=%d)", scans)
	}
}

// Codex round 3 #1: a caller that joins a running scan must not get a
// "closed" answer when that scan's result is older than the window by the
// time it wakes (a stalled scan).
func TestScanGateStaleJoinedScanOpens(t *testing.T) {
	defer setNegativeTTL(time.Second)()
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	started := make(chan struct{})
	release := make(chan struct{})
	g := newScanGate(func() (bool, error) { close(started); <-release; return false, nil }, nil)
	g.now = clock
	first := make(chan bool, 1)
	go func() { first <- g.Active() }()
	<-started
	joined := make(chan bool, 1)
	go func() { joined <- g.Active() }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		g.access.Lock()
		w := g.waiting
		g.access.Unlock()
		if w == 1 {
			break // the second caller is parked on the running scan
		}
		if time.Now().After(deadline) {
			t.Fatal("the second caller never joined the running scan")
		}
		runtime.Gosched()
	}
	mu.Lock()
	now = now.Add(1500 * time.Millisecond) // the scan stalls past the window
	mu.Unlock()
	close(release)
	if !<-joined || !<-first {
		t.Fatal("a negative result older than the window must open the gate")
	}
}
