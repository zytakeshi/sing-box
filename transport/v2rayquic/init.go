//go:build with_quic

package v2rayquic

import "github.com/zytakeshi/vpncheap-core/transport/v2ray"

func init() {
	v2ray.RegisterQUICConstructor(NewServer, NewClient)
}
