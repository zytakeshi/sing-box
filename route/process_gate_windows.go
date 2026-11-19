//go:build windows

package route

import (
	"strings"
	"unsafe"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

type windowsProcessNameGate struct {
	names map[string]struct{}
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	w := &windowsProcessNameGate{names: lowerNameSet(names)}
	return newScanGate(w.scan, logger)
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
