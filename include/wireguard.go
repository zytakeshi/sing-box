//go:build with_wireguard

package include

import (
	"github.com/zytakeshi/vpncheap-core/adapter/endpoint"
	"github.com/zytakeshi/vpncheap-core/adapter/outbound"
	"github.com/zytakeshi/vpncheap-core/protocol/wireguard"
)

func registerWireGuardOutbound(registry *outbound.Registry) {
	wireguard.RegisterOutbound(registry)
}

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
