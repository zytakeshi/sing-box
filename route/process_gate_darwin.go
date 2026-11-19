//go:build darwin && !ios

package route

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/unix"
)

// macOS: one process lookup (process.FindDarwinConnectionOwner, also behind
// the app's platform connection-owner finder) dumps the whole TCP pcblist via
// sysctl. Measured 13-15x the core CPU per connection with the BitTorrent
// rule (VilaNet tickets 20279/20280). The gate lists the processes
// (sysctl kern.proc.all) once per window and reads each executable path.
//
// The process_name rule matches filepath.Base of the path that
// FindDarwinConnectionOwner reads with proc_pidpath, so the scan reads the
// same value with the same call (p_comm can differ: a symlinked or renamed
// executable). A pid whose path the call cannot read for a stable reason is
// skipped: the process is gone (ESRCH), its executable has no path any more
// (ENOENT: deleted or replaced while running), or access is denied
// (EPERM/EACCES) - the searcher gets the same error, so the rule cannot match
// that process. Any other error fails the scan, and the gate opens.
// Measured inside the App Sandbox: 766 processes, all calls allowed, 1.6 ms
// per scan; on a desktop 3 ENOENT + 2 ESRCH of 781.

type darwinProcessNameGate struct {
	names map[string]struct{} // lower-case rule names
	path  func(pid int32, buf []byte) (string, error)
}

func newProcessNameGate(names []string, logger log.ContextLogger) processNameGate {
	return newScanGate((&darwinProcessNameGate{names: lowerNameSet(names), path: darwinProcessPath}).scan, logger)
}

func (d *darwinProcessNameGate) scan() (bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return false, E.Cause(err, "sysctl kern.proc.all")
	}
	buf := make([]byte, darwinProcPathSize)
	for i := range procs {
		pid := procs[i].Proc.P_pid
		if pid <= 0 {
			continue
		}
		path, err := d.path(pid, buf)
		switch err {
		case nil:
			if d.matches(filepath.Base(path)) {
				return true, nil
			}
		case syscall.ESRCH, syscall.ENOENT, syscall.EPERM, syscall.EACCES:
			// stable: the searcher cannot read this process either
		default:
			return false, E.Cause(err, "proc_pidpath ", pid)
		}
	}
	return false, nil
}

// matches is case-insensitive: a superset of the rule's exact match.
func (d *darwinProcessNameGate) matches(base string) bool {
	if base == "" {
		return false
	}
	_, ok := d.names[strings.ToLower(base)]
	return ok
}

const darwinProcPathSize = 4096 // PROC_PIDPATHINFO_MAXSIZE

// darwinProcessPath is proc_pidpath, the call of getExecPathFromPID in
// common/process (searcher_darwin_shared.go).
func darwinProcessPath(pid int32, buf []byte) (string, error) {
	const (
		procCallPIDInfo = 0x2
		procPIDPathInfo = 0xb
	)
	_, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procCallPIDInfo, uintptr(pid), procPIDPathInfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(buf), nil
}
