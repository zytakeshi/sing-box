package route

import (
	"strings"

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
