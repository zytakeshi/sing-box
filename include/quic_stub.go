//go:build !with_quic

package include

import (
	"context"
	"io"
	"net/http"

	"github.com/zytakeshi/vpncheap-core/adapter"
	"github.com/zytakeshi/vpncheap-core/adapter/inbound"
	"github.com/zytakeshi/vpncheap-core/adapter/outbound"
	"github.com/zytakeshi/vpncheap-core/common/listener"
	"github.com/zytakeshi/vpncheap-core/common/tls"
	C "github.com/zytakeshi/vpncheap-core/constant"
	"github.com/zytakeshi/vpncheap-core/log"
	"github.com/zytakeshi/vpncheap-core/option"
	"github.com/zytakeshi/vpncheap-core/protocol/naive"
	"github.com/zytakeshi/vpncheap-core/transport/v2ray"
	"github.com/sagernet/sing-dns"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func init() {
	dns.RegisterTransport([]string{"quic", "h3"}, func(options dns.TransportOptions) (dns.Transport, error) {
		return nil, C.ErrQUICNotIncluded
	})
	v2ray.RegisterQUICConstructor(
		func(ctx context.Context, logger logger.ContextLogger, options option.V2RayQUICOptions, tlsConfig tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (adapter.V2RayServerTransport, error) {
			return nil, C.ErrQUICNotIncluded
		},
		func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayQUICOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
			return nil, C.ErrQUICNotIncluded
		},
	)
}

func registerQUICInbounds(registry *inbound.Registry) {
	inbound.Register[option.HysteriaInboundOptions](registry, C.TypeHysteria, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HysteriaInboundOptions) (adapter.Inbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
	inbound.Register[option.TUICInboundOptions](registry, C.TypeTUIC, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICInboundOptions) (adapter.Inbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
	inbound.Register[option.Hysteria2InboundOptions](registry, C.TypeHysteria2, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2InboundOptions) (adapter.Inbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
	naive.ConfigureHTTP3ListenerFunc = func(listener *listener.Listener, handler http.Handler, tlsConfig tls.ServerConfig, logger logger.Logger) (io.Closer, error) {
		return nil, C.ErrQUICNotIncluded
	}
}

func registerQUICOutbounds(registry *outbound.Registry) {
	outbound.Register[option.HysteriaOutboundOptions](registry, C.TypeHysteria, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HysteriaOutboundOptions) (adapter.Outbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
	outbound.Register[option.TUICOutboundOptions](registry, C.TypeTUIC, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICOutboundOptions) (adapter.Outbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
	outbound.Register[option.Hysteria2OutboundOptions](registry, C.TypeHysteria2, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2OutboundOptions) (adapter.Outbound, error) {
		return nil, C.ErrQUICNotIncluded
	})
}
