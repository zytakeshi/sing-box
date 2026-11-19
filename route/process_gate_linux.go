//go:build linux && !android

package route

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// Linux: one process lookup maps the socket to its owner through /proc for
// every new connection; with the BitTorrent process_name rule that measured
// 25-33x the core CPU per connection (VilaNet tickets 20279/20280). The gate
// reads the executable of every process once per window instead.
//
// The process_name rule matches filepath.Base of readlink /proc/<pid>/exe,
// the value the searcher reads (searcher_linux_shared.go), so the scan reads
// the same link (comm can differ: prctl PR_SET_NAME, 15-byte cut). A pid
// whose link cannot be read is skipped: the searcher cannot read it either,
// so the rule cannot match that process.

type linuxProcessNameGate struct {
	names map[string]struct{} // lower-case rule names
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	return newScanGate((&linuxProcessNameGate{names: lowerNameSet(names)}).scan, logger)
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
		exe, err := os.Readlink(filepath.Join("/proc", name, "exe"))
		if err != nil {
			continue // exited, a kernel thread, or not readable for the searcher either
		}
		if g.matches(filepath.Base(exe)) {
			return true, nil
		}
	}
	return false, nil
}

// matches is case-insensitive: a superset of the rule's exact match.
func (g *linuxProcessNameGate) matches(base string) bool {
	if base == "" {
		return false
	}
	_, ok := g.names[strings.ToLower(base)]
	return ok
}
