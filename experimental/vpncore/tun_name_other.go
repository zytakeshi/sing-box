//go:build !(darwin || linux)

package vpncore

import "os"

func getTunnelName(fd int32) (string, error) {
	return "", os.ErrInvalid
}
