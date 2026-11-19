package route

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// No-regression gate for the Windows TUN CPU cost (VilaNet tickets 20279/20280,
// founder order 2026-11-19: "the high cpu usage thing must never regress").
//
// On Windows one process lookup dumps the whole TCP table. 2.17.2 did that for
// every new connection while a process_name rule existed (~7x core CPU); the
// "exact" gate of the unreleased 2.17.5 took one process snapshot per
// connection (~6x). This test counts both OS calls for 1,000 new connections
// inside one second and fails above a small fixed bound. It drives the same
// code as Router.Start (setupProcessLookup) and the routing path
// (searchProcessInfo), with a fake clock and fake OS calls, so it has no
// timing noise. VilaNet CI runs it against the DLL's source commit.

const (
	costTestConnections = 1000
	// At most one scan per second plus the first one.
	costTestMaxScansPerSecond = 2
)

type countingSearcher struct{ lookups atomic.Int64 }

func (s *countingSearcher) FindProcessInfo(_ context.Context, _ string, _ netip.AddrPort, _ netip.AddrPort) (*adapter.ConnectionOwner, error) {
	s.lookups.Add(1)
	return &adapter.ConnectionOwner{ProcessPath: `C:\Program Files\Browser\browser.exe`, UserId: -1}, nil
}

func (s *countingSearcher) Close() error { return nil }

func btProcessRule() option.Rule {
	return option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
		RawDefaultRule: option.RawDefaultRule{ProcessName: []string{"qbittorrent.exe", "uTorrent.exe", "BitTorrent.exe", "BitComet.exe"}},
		RuleAction:     option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: "direct"}},
	}}
}

// runCostTest sends costTestConnections new loopback connections through the
// routing path within one second of fake time and returns the TCP-table
// lookups and process scans they cost.
func runCostTest(t *testing.T, rules []option.Rule, listedProgramRuns bool) (lookups int64, scans int64, searcherCreated bool) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	var scanCount atomic.Int64
	oldFactory := processNameGateFactory
	processNameGateFactory = func(names []string, logger log.ContextLogger) processNameGate {
		g := newScanGate(func() (bool, error) { scanCount.Add(1); return listedProgramRuns, nil }, logger)
		g.now = func() time.Time { return now }
		return g
	}
	defer func() { processNameGateFactory = oldFactory }()

	r := NewRouter(context.Background(), log.NewNOPFactory(), option.RouteOptions{Rules: rules}, option.DNSOptions{})
	if !r.needFindProcess {
		return 0, 0, false
	}
	searcher := &countingSearcher{}
	r.processSearcher = searcher // what Start does on Windows (process.NewSearcher)
	r.setupProcessLookup()
	for i := 0; i < costTestConnections; i++ {
		metadata := &adapter.InboundContext{
			Network:     N.NetworkTCP,
			Source:      M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), uint16(20000+i)),
			Destination: M.SocksaddrFrom(netip.MustParseAddr("203.0.113.10"), 443),
		}
		r.searchProcessInfo(context.Background(), metadata)
		now = now.Add(time.Second / costTestConnections) // 1,000 connections in 1 s
	}
	return searcher.lookups.Load(), scanCount.Load(), true
}

func TestProcessGateBoundsOSScans(t *testing.T) {
	t.Run("rule on, no listed program", func(t *testing.T) {
		lookups, scans, created := runCostTest(t, []option.Rule{btProcessRule()}, false)
		if !created {
			t.Fatal("a process_name rule must create the process searcher")
		}
		t.Logf("lookups=%d scans=%d for %d connections", lookups, scans, costTestConnections)
		if lookups != 0 {
			t.Fatalf("CPU REGRESSION: %d TCP-table lookups for %d new connections while no listed program runs (want 0)", lookups, costTestConnections)
		}
		if scans > costTestMaxScansPerSecond {
			t.Fatalf("CPU REGRESSION: %d process scans for %d new connections in 1 s (want <= %d)", scans, costTestConnections, costTestMaxScansPerSecond)
		}
	})
	t.Run("rule on, listed program runs", func(t *testing.T) {
		lookups, scans, _ := runCostTest(t, []option.Rule{btProcessRule()}, true)
		t.Logf("lookups=%d scans=%d", lookups, scans)
		// While a BitTorrent client runs, every connection must be looked up,
		// or the rule cannot match it.
		if lookups != costTestConnections {
			t.Fatalf("a running listed program must be matched: %d lookups for %d connections", lookups, costTestConnections)
		}
		if scans > costTestMaxScansPerSecond {
			t.Fatalf("CPU REGRESSION: %d process scans in 1 s (want <= %d)", scans, costTestMaxScansPerSecond)
		}
	})
	t.Run("rule off", func(t *testing.T) {
		_, _, created := runCostTest(t, nil, false)
		if created {
			t.Fatal("without process rules no process searcher may exist (0 OS calls)")
		}
	})
}
