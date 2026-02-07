package include

import (
	"context"

	"github.com/zytakeshi/vpncheap-core/adapter"
	"github.com/zytakeshi/vpncheap-core/adapter/endpoint"
	"github.com/zytakeshi/vpncheap-core/adapter/inbound"
	"github.com/zytakeshi/vpncheap-core/adapter/outbound"
	C "github.com/zytakeshi/vpncheap-core/constant"
	"github.com/zytakeshi/vpncheap-core/log"
	"github.com/zytakeshi/vpncheap-core/option"
	"github.com/zytakeshi/vpncheap-core/protocol/block"
	"github.com/zytakeshi/vpncheap-core/protocol/direct"
	"github.com/zytakeshi/vpncheap-core/protocol/dns"
	"github.com/zytakeshi/vpncheap-core/protocol/group"
	"github.com/zytakeshi/vpncheap-core/protocol/http"
	"github.com/zytakeshi/vpncheap-core/protocol/mixed"
	"github.com/zytakeshi/vpncheap-core/protocol/naive"
	"github.com/zytakeshi/vpncheap-core/protocol/redirect"
	"github.com/zytakeshi/vpncheap-core/protocol/shadowsocks"
	"github.com/zytakeshi/vpncheap-core/protocol/shadowtls"
	"github.com/zytakeshi/vpncheap-core/protocol/socks"
	"github.com/zytakeshi/vpncheap-core/protocol/ssh"
	"github.com/zytakeshi/vpncheap-core/protocol/tor"
	"github.com/zytakeshi/vpncheap-core/protocol/trojan"
	"github.com/zytakeshi/vpncheap-core/protocol/tun"
	"github.com/zytakeshi/vpncheap-core/protocol/vless"
	"github.com/zytakeshi/vpncheap-core/protocol/vmess"
	E "github.com/sagernet/sing/common/exceptions"
)

func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()

	tun.RegisterInbound(registry)
	redirect.RegisterRedirect(registry)
	redirect.RegisterTProxy(registry)
	direct.RegisterInbound(registry)

	socks.RegisterInbound(registry)
	http.RegisterInbound(registry)
	mixed.RegisterInbound(registry)

	shadowsocks.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	naive.RegisterInbound(registry)
	shadowtls.RegisterInbound(registry)
	vless.RegisterInbound(registry)

	registerQUICInbounds(registry)
	registerStubForRemovedInbounds(registry)

	return registry
}

func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()

	direct.RegisterOutbound(registry)

	block.RegisterOutbound(registry)
	dns.RegisterOutbound(registry)

	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)

	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	tor.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)

	registerQUICOutbounds(registry)
	registerWireGuardOutbound(registry)
	registerStubForRemovedOutbounds(registry)

	return registry
}

func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()

	registerWireGuardEndpoint(registry)

	return registry
}

func registerStubForRemovedInbounds(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in v1.6.0")
	})
}

func registerStubForRemovedOutbounds(registry *outbound.Registry) {
	outbound.Register[option.ShadowsocksROutboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksROutboundOptions) (adapter.Outbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in v1.6.0")
	})
}
