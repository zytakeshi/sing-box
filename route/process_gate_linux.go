//go:build linux && !android

package route

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
// whose link cannot be read for a stable reason is skipped: gone or a kernel
// thread (ENOENT/ESRCH) or access denied (EACCES/EPERM) - the searcher gets
// the same error, so the rule cannot match that process. Any other error
// fails the scan, and the gate opens.

type linuxProcessNameGate struct {
	names    map[string]struct{} // lower-case rule names
	readlink func(string) (string, error)
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	return newScanGate((&linuxProcessNameGate{names: lowerNameSet(names), readlink: os.Readlink}).scan, logger)
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
		exe, err := g.readlink(filepath.Join("/proc", name, "exe"))
		switch {
		case err == nil:
			if g.matches(filepath.Base(exe)) {
				return true, nil
			}
		case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ESRCH),
			errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
			// stable: the searcher cannot read this process either
		default:
			return false, E.Cause(err, "readlink ", name, "/exe")
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
