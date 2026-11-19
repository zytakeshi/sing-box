//go:build linux && !android

package route

import (
	"os"
	"strings"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// Linux: one process lookup maps the socket to its owner through /proc for
// every new connection; with the BitTorrent process_name rule that measured
// 33x the core CPU per connection (VilaNet tickets 20279/20280). The gate
// reads /proc/<pid>/comm of every process once per window instead.

// /proc/<pid>/comm keeps TASK_COMM_LEN-1 = 15 characters.
const linuxCommLen = 15

type linuxProcessNameGate struct {
	names []string // lower-case rule names
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	g := &linuxProcessNameGate{}
	for name := range lowerNameSet(names) {
		g.names = append(g.names, name)
	}
	return newScanGate(g.scan, logger)
}

func (g *linuxProcessNameGate) scan() (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, E.Cause(err, "read /proc")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		comm, err := os.ReadFile("/proc/" + name + "/comm")
		if err != nil {
			continue // the process exited meanwhile
		}
		if g.matches(strings.TrimRight(string(comm), "\n")) {
			return true, nil
		}
	}
	return false, nil
}

// matches compares a (possibly truncated) comm with the rule names,
// case-insensitive. A truncated comm matches every rule name it is a prefix
// of: a superset, so the gate can only open more often, never hide a match.
func (g *linuxProcessNameGate) matches(comm string) bool {
	if comm == "" {
		return false
	}
	comm = strings.ToLower(comm)
	for _, name := range g.names {
		if name == comm || (len(comm) >= linuxCommLen && strings.HasPrefix(name, comm)) {
			return true
		}
	}
	return false
}
