package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio/deadline"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	mDNS "github.com/miekg/dns"
)

var _ adapter.DNSTransport = (*UDPTransport)(nil)

func RegisterUDP(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.RemoteDNSServerOptions](registry, C.DNSTypeUDP, NewUDP)
}

type UDPTransport struct {
	dns.TransportAdapter
	logger logger.ContextLogger

	dialer     N.Dialer
	serverAddr M.Socksaddr
	udpSize    atomic.Int32

	connection *ConnPool[*udpConn]

	callbackAccess sync.RWMutex
	queryId        uint16
	callbacks      map[uint16]*udpCallback
}

// udpConn carries a per-socket read counter alongside the connection. recvLoop
// bumps readEpoch on every packet successfully read from THIS socket; a query
// that times out while its socket's epoch has not moved proves nothing arrived
// there for the whole request window, so the socket is assumed silently dead
// (WAN redial / NAT mapping expiry) and gets invalidated instead of returned to
// the pool. Keeping the counter on the connection rather than the transport
// means a late read on a superseded generation cannot mask a dead successor.
type udpConn struct {
	net.Conn
	readEpoch atomic.Uint64
}

// errUDPSharedConnInvalidated is the cancellation cause handed to the pool when
// a quiet deadline evicts the shared socket. The query that triggered the
// eviction still returns its own ctx.Err(); siblings waiting on the same socket
// observe this instead, so a peer's eviction is never mistaken for their own
// deadline in logs or retry policy.
var errUDPSharedConnInvalidated = errors.New("UDP DNS shared connection invalidated after idle deadline")

type udpCallback struct {
	access   sync.Mutex
	response *mDNS.Msg
	done     chan struct{}
}

func NewUDP(ctx context.Context, logger log.ContextLogger, tag string, options option.RemoteDNSServerOptions) (adapter.DNSTransport, error) {
	transportDialer, err := dns.NewRemoteDialer(ctx, options)
	if err != nil {
		return nil, err
	}
	serverAddr := options.DNSServerAddressOptions.Build()
	if serverAddr.Port == 0 {
		serverAddr.Port = 53
	}
	if !serverAddr.IsValid() {
		return nil, E.New("invalid server address: ", serverAddr)
	}
	return NewUDPRaw(logger, dns.NewTransportAdapterWithRemoteOptions(C.DNSTypeUDP, tag, options), transportDialer, serverAddr), nil
}

func NewUDPRaw(logger logger.ContextLogger, adapter dns.TransportAdapter, dialerInstance N.Dialer, serverAddr M.Socksaddr) *UDPTransport {
	t := &UDPTransport{
		TransportAdapter: adapter,
		logger:           logger,
		dialer:           dialerInstance,
		serverAddr:       serverAddr,
		callbacks:        make(map[uint16]*udpCallback),
		connection: NewConnPool(ConnPoolOptions[*udpConn]{
			Mode: ConnPoolSingle,
			IsAlive: func(conn *udpConn) bool {
				return conn != nil
			},
			Close: func(conn *udpConn, cause error) {
				conn.Close()
			},
		}),
	}
	t.udpSize.Store(2048)
	return t
}

func (t *UDPTransport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return dialer.InitializeDetour(t.dialer)
}

func (t *UDPTransport) Close() error {
	return t.connection.Close()
}

func (t *UDPTransport) Reset() {
	t.connection.Reset()
}

func (t *UDPTransport) nextAvailableQueryId() (uint16, error) {
	start := t.queryId
	for {
		t.queryId++
		if _, exists := t.callbacks[t.queryId]; !exists {
			return t.queryId, nil
		}
		if t.queryId == start {
			return 0, E.New("no available query ID")
		}
	}
}

func (t *UDPTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	response, err := t.exchange(ctx, message)
	if err != nil {
		return nil, err
	}
	if response.Truncated {
		t.logger.InfoContext(ctx, "response truncated, retrying with TCP")
		return t.exchangeTCP(ctx, message)
	}
	return response, nil
}

func (t *UDPTransport) exchangeTCP(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	conn, err := t.dialer.DialContext(ctx, N.NetworkTCP, t.serverAddr)
	if err != nil {
		return nil, E.Cause(err, "dial TCP connection")
	}
	defer conn.Close()
	defer setConnDeadline(ctx, conn, deadline.NeedAdditionalReadDeadline(conn))()
	err = WriteMessage(conn, message.Id, message)
	if err != nil {
		return nil, E.Cause(err, "write request")
	}
	response, err := ReadMessage(conn)
	if err != nil {
		return nil, E.Cause(err, "read response")
	}
	return response, nil
}

func (t *UDPTransport) exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if edns0Opt := message.IsEdns0(); edns0Opt != nil {
		udpSize := int32(edns0Opt.UDPSize())
		for {
			current := t.udpSize.Load()
			if udpSize <= current {
				break
			}
			if t.udpSize.CompareAndSwap(current, udpSize) {
				t.Reset()
				break
			}
		}
	}

	callback := &udpCallback{
		done: make(chan struct{}),
	}

	t.callbackAccess.Lock()
	queryId, err := t.nextAvailableQueryId()
	if err != nil {
		t.callbackAccess.Unlock()
		return nil, err
	}
	t.callbacks[queryId] = callback
	t.callbackAccess.Unlock()

	defer func() {
		t.callbackAccess.Lock()
		delete(t.callbacks, queryId)
		t.callbackAccess.Unlock()
	}()

	buffer := buf.NewSize(1 + message.Len())
	defer buffer.Release()

	exMessage := *message
	exMessage.Compress = true
	originalId := message.Id
	exMessage.Id = queryId

	rawMessage, err := exMessage.PackBuffer(buffer.FreeBytes())
	if err != nil {
		return nil, err
	}

	var (
		conn         *udpConn
		connCtx      context.Context
		readEpoch    uint64
		firstAttempt = true
	)
	for {
		var created bool
		conn, connCtx, created, err = t.connection.AcquireShared(ctx, func(ctx context.Context) (*udpConn, error) {
			rawConn, err := t.dialer.DialContext(ctx, N.NetworkUDP, t.serverAddr)
			if err != nil {
				return nil, E.Cause(err, "dial UDP connection")
			}
			return &udpConn{Conn: rawConn}, nil
		})
		if err != nil {
			return nil, err
		}
		if created {
			go t.recvLoop(conn)
		}

		readEpoch = conn.readEpoch.Load()

		_, err = conn.Write(rawMessage)
		if err == nil {
			break
		}
		t.connection.Invalidate(conn, err)
		// A write failure on a socket we did NOT dial ourselves usually means a
		// sibling already tore it down (or the kernel did) between acquire and
		// write — the query never reached the wire, so failing it would report a
		// stale peer's problem as ours. Retry exactly once against whatever the
		// pool hands back, which is a freshly dialed socket now the old one is
		// invalidated. A conn we just dialed, a second failure, or an expired
		// caller context all fail immediately. Mirrors upstream 1.14's
		// `if !created && firstAttempt { continue }`.
		if created || !firstAttempt || ctx.Err() != nil {
			return nil, E.Cause(err, "write request")
		}
		firstAttempt = false
	}

	select {
	case <-callback.done:
		t.connection.Release(conn, true)
		callback.response.Id = originalId
		return callback.response, nil
	case <-connCtx.Done():
		return nil, context.Cause(connCtx)
	case <-ctx.Done():
		err = ctx.Err()
		// A real deadline with no read progress on this socket since the query
		// was written means the socket is silently dead: drop it so the next
		// query redials. Plain cancellation says nothing about the socket, so
		// it still goes back to the pool.
		//
		// The eviction cause is deliberately NOT this query's ctx.Err(): it
		// becomes connCtx's cancellation cause for every sibling still waiting
		// on this socket, and their own deadlines have not expired.
		if errors.Is(err, context.DeadlineExceeded) && conn.readEpoch.Load() == readEpoch {
			t.connection.Invalidate(conn, errUDPSharedConnInvalidated)
		} else {
			t.connection.Release(conn, true)
		}
		return nil, err
	}
}

func (t *UDPTransport) recvLoop(conn *udpConn) {
	for {
		buffer := buf.NewSize(int(t.udpSize.Load()))
		_, err := buffer.ReadOnceFrom(conn)
		if err != nil {
			buffer.Release()
			t.connection.Invalidate(conn, err)
			return
		}
		conn.readEpoch.Add(1)

		var message mDNS.Msg
		err = message.Unpack(buffer.Bytes())
		buffer.Release()
		if err != nil {
			t.logger.Debug("discarded malformed UDP response: ", err)
			continue
		}

		t.callbackAccess.RLock()
		callback, loaded := t.callbacks[message.Id]
		t.callbackAccess.RUnlock()

		if !loaded {
			continue
		}

		callback.access.Lock()
		select {
		case <-callback.done:
		default:
			callback.response = &message
			close(callback.done)
		}
		callback.access.Unlock()
	}
}
