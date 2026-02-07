//go:build !with_wireguard

package include

import (
	"context"

	"github.com/zytakeshi/vpncheap-core/adapter"
	"github.com/zytakeshi/vpncheap-core/adapter/endpoint"
	"github.com/zytakeshi/vpncheap-core/adapter/outbound"
	C "github.com/zytakeshi/vpncheap-core/constant"
	"github.com/zytakeshi/vpncheap-core/log"
	"github.com/zytakeshi/vpncheap-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerWireGuardOutbound(registry *outbound.Registry) {
	outbound.Register[option.LegacyWireGuardOutboundOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.LegacyWireGuardOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`WireGuard is not included in this build, rebuild with -tags with_wireguard`)
	})
}

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`WireGuard is not included in this build, rebuild with -tags with_wireguard`)
	})
}
