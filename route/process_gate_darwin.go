//go:build darwin && !ios

package route

import (
	"strings"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/unix"
)

// macOS: one process lookup (process.FindDarwinConnectionOwner, also behind
// the app's platform connection-owner finder) dumps the whole TCP pcblist via
// sysctl. Measured 13x the core CPU per connection with the BitTorrent rule
// (VilaNet tickets 20279/20280). The gate scans the process list once per
// window with one sysctl (kern.proc.all).

// macOS keeps only the first maxComLen characters of a process name in
// kinfo_proc (p_comm).
const maxComLen = 16

type darwinProcessNameGate struct {
	names []string // lower-case rule names
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	set := lowerNameSet(names)
	d := &darwinProcessNameGate{}
	for name := range set {
		d.names = append(d.names, name)
	}
	return newScanGate(d.scan, logger)
}

func (d *darwinProcessNameGate) scan() (bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return false, E.Cause(err, "sysctl kern.proc.all")
	}
	for i := range procs {
		if d.matches(commName(procs[i].Proc.P_comm[:])) {
			return true, nil
		}
	}
	return false, nil
}

// matches compares a (possibly truncated) process name with the rule names,
// case-insensitive. A truncated name matches every rule name it is a prefix
// of: a superset, so the gate can only open more often, never hide a match.
func (d *darwinProcessNameGate) matches(comm string) bool {
	if comm == "" {
		return false
	}
	comm = strings.ToLower(comm)
	for _, name := range d.names {
		if name == comm || (len(comm) >= maxComLen && strings.HasPrefix(name, comm)) {
			return true
		}
	}
	return false
}

func commName(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
