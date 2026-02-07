package wireguard

import (
	"github.com/zytakeshi/vpncheap-core/common/dialer"
	"github.com/sagernet/wireguard-go/conn"
)

func init() {
	dialer.WgControlFns = conn.ControlFns
}
