//go:build windows

package route

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

const processNameGateInterval = time.Second

type windowsProcessNameGate struct {
	logger    log.ContextLogger
	names     map[string]struct{}
	access    sync.Mutex
	checkedAt time.Time
	active    bool
	failed    bool
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	return &windowsProcessNameGate{logger: logger, names: lowerNameSet(names)}
}

// Active scans the process list at most once per interval. A failed scan
// opens the gate (the old behaviour: look up every connection) and is logged
// once per failure streak.
func (g *windowsProcessNameGate) Active() bool {
	g.access.Lock()
	defer g.access.Unlock()
	now := time.Now()
	if !g.checkedAt.IsZero() && now.Sub(g.checkedAt) < processNameGateInterval {
		return g.active
	}
	g.checkedAt = now
	active, err := g.scan()
	if err != nil {
		if !g.failed {
			g.logger.Warn(E.Cause(err, "process gate: scan the process list (every connection is looked up until a scan works)"))
		}
		g.failed = true
		g.active = true
		return true
	}
	if g.failed {
		g.logger.Info("process gate: process list scan works again")
		g.failed = false
	}
	if active != g.active {
		g.logger.Debug("process gate: a listed process runs = ", active)
	}
	g.active = active
	return active
}

func (g *windowsProcessNameGate) scan() (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, E.Cause(err, "CreateToolhelp32Snapshot")
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		if _, listed := g.names[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))]; listed {
			return true, nil
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if err == windows.ERROR_NO_MORE_FILES {
		return false, nil
	}
	return false, E.Cause(err, "Process32Next")
}
