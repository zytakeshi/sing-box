package route

import (
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// processNameGate lets the router skip the per-connection process search while
// none of the programs named by process_name rules runs.
//
// Why (VilaNet ticket 20280): on Windows every lookup dumps the whole TCP
// table (GetExtendedTcpTable). One process_name rule made each new TUN
// connection pay that dump, about 10x the core CPU per connection on a busy
// desktop. When every process condition is a plain process_name list, a rule
// can only match while one of those programs runs; one process snapshot per
// interval answers that for all connections.
type processNameGate interface {
	// Active reports whether a lookup can change a rule result now.
	Active() bool
}

// processNameGateNames returns the process_name values of all route and DNS
// rules (logical rules included). ok is false when any rule uses another
// process condition (path, path regex, package, user), because the gate
// cannot answer for those.
func processNameGateNames(rules []option.Rule, dnsRules []option.DNSRule) (names []string, ok bool) {
	ok = true
	var walk func([]option.Rule)
	walk = func(rules []option.Rule) {
		for _, rule := range rules {
			switch rule.Type {
			case C.RuleTypeDefault:
				o := rule.DefaultOptions
				if len(o.ProcessPath) > 0 || len(o.ProcessPathRegex) > 0 || len(o.PackageName) > 0 || len(o.User) > 0 || len(o.UserID) > 0 {
					ok = false
				}
				names = append(names, o.ProcessName...)
			case C.RuleTypeLogical:
				walk(rule.LogicalOptions.Rules)
			}
		}
	}
	var walkDNS func([]option.DNSRule)
	walkDNS = func(rules []option.DNSRule) {
		for _, rule := range rules {
			switch rule.Type {
			case C.RuleTypeDefault:
				o := rule.DefaultOptions
				if len(o.ProcessPath) > 0 || len(o.ProcessPathRegex) > 0 || len(o.PackageName) > 0 || len(o.User) > 0 || len(o.UserID) > 0 {
					ok = false
				}
				names = append(names, o.ProcessName...)
			case C.RuleTypeLogical:
				walkDNS(rule.LogicalOptions.Rules)
			}
		}
	}
	walk(rules)
	walkDNS(dnsRules)
	for _, name := range names {
		// The Windows searcher names PID 0/4 ":System Idle Process" / ":System";
		// no process snapshot carries those names, so the gate cannot answer.
		if strings.HasPrefix(name, ":") {
			ok = false
		}
	}
	return names, ok
}

// lowerNameSet folds names for a case-insensitive compare. The rule itself
// compares exactly; a case-insensitive gate is a superset, so it can only
// open more often than needed, never hide a match.
func lowerNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[strings.ToLower(name)] = struct{}{}
	}
	return set
}

// A scan that found a listed program keeps the gate open this long: a lookup
// is authoritative anyway, so a stale "open" only costs some lookups.
const processNameGatePositiveTTL = time.Second

// scanGate answers Active from a process-list scan.
//
// It never answers "closed" from a scan older than the call: a "closed"
// answer needs a scan that STARTED after the call began, so a listed program
// that started just before its first connection is always seen. Concurrent
// callers share one scan, so a connection storm costs at most one process
// snapshot at a time, never one TCP-table dump per connection. A failed scan
// opens the gate (every connection is looked up, the old behaviour) and is
// logged once per failure streak.
type scanGate struct {
	scan   func() (bool, error)
	logger log.ContextLogger
	now    func() time.Time

	access     sync.Mutex
	done       *sync.Cond
	scanning   bool
	lastStart  time.Time
	lastActive bool
	failed     bool
}

func newScanGate(scan func() (bool, error), logger log.ContextLogger) *scanGate {
	g := &scanGate{scan: scan, logger: logger, now: time.Now}
	g.done = sync.NewCond(&g.access)
	return g
}

func (g *scanGate) Active() bool {
	arrival := g.now()
	g.access.Lock()
	defer g.access.Unlock()
	for {
		if !g.lastStart.IsZero() {
			if g.lastActive && arrival.Sub(g.lastStart) < processNameGatePositiveTTL {
				return true
			}
			if !g.lastStart.Before(arrival) {
				return g.lastActive
			}
		}
		if g.scanning {
			g.done.Wait()
			continue
		}
		g.scanning = true
		start := g.now()
		g.access.Unlock()
		active, err := g.scan()
		g.access.Lock()
		g.scanning = false
		if err != nil {
			if !g.failed && g.logger != nil {
				g.logger.Warn(E.Cause(err, "process gate: scan the process list (every connection is looked up until a scan works)"))
			}
			g.failed = true
			active = true
		} else if g.failed {
			if g.logger != nil {
				g.logger.Info("process gate: process list scan works again")
			}
			g.failed = false
		}
		g.lastStart = start
		g.lastActive = active
		g.done.Broadcast()
	}
}
