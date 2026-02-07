//go:build with_quic

package include

import (
	"github.com/zytakeshi/vpncheap-core/adapter/inbound"
	"github.com/zytakeshi/vpncheap-core/adapter/outbound"
	"github.com/zytakeshi/vpncheap-core/protocol/hysteria"
	"github.com/zytakeshi/vpncheap-core/protocol/hysteria2"
	_ "github.com/zytakeshi/vpncheap-core/protocol/naive/quic"
	"github.com/zytakeshi/vpncheap-core/protocol/tuic"
	_ "github.com/zytakeshi/vpncheap-core/transport/v2rayquic"
	_ "github.com/sagernet/sing-dns/quic"
)

func registerQUICInbounds(registry *inbound.Registry) {
	hysteria.RegisterInbound(registry)
	tuic.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
}

func registerQUICOutbounds(registry *outbound.Registry) {
	hysteria.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
}
