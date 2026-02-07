//go:build !windows

package vpncore

import "syscall"

func dup(fd int) (nfd int, err error) {
	return syscall.Dup(fd)
}
