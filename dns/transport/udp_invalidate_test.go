package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	mDNS "github.com/miekg/dns"
)

// fakeUpstream is a real loopback UDP DNS responder with test-driven policy.
//
// Two knobs model the failure modes under test:
//
//   - oneShot: answer only the FIRST datagram from a given client socket, then
//     go silent for it forever. That is the NAT-mapping-expiry / WAN-redial
//     shape from ticket 18766 — the socket looks open but nothing comes back.
//   - drop: never answer these question names, whatever the socket.
//
// Client identity is (re)declared explicitly by the dialer via registerClient
// rather than inferred purely from the source address, so an OS that recycles a
// closed UDP source port cannot make a freshly dialed socket inherit the dead
// one's "already answered" state.
type fakeUpstream struct {
	conn *net.UDPConn

	access   sync.Mutex
	oneShot  bool
	answered map[string]bool
	drop     map[string]bool

	received chan string // every question name the server read off the wire
}

func newFakeUpstream(t *testing.T, oneShot bool, drop ...string) *fakeUpstream {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeUpstream{
		conn:     conn,
		oneShot:  oneShot,
		answered: make(map[string]bool),
		drop:     make(map[string]bool),
		received: make(chan string, 256),
	}
	for _, name := range drop {
		s.drop[name] = true
	}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

// registerClient marks addr as a fresh client generation. Called by the dialer
// after every successful dial, so a recycled source port starts clean.
func (s *fakeUpstream) registerClient(addr net.Addr) {
	s.access.Lock()
	defer s.access.Unlock()
	s.answered[addr.String()] = false
}

// shouldAnswer applies the policy and records the one-shot state transition.
func (s *fakeUpstream) shouldAnswer(addr net.Addr, question string) bool {
	s.access.Lock()
	defer s.access.Unlock()
	if s.drop[question] {
		return false
	}
	if !s.oneShot {
		return true
	}
	if s.answered[addr.String()] {
		return false
	}
	s.answered[addr.String()] = true
	return true
}

func (s *fakeUpstream) serve() {
	buffer := make([]byte, 2048)
	for {
		n, addr, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		var request mDNS.Msg
		if request.Unpack(buffer[:n]) != nil || len(request.Question) == 0 {
			continue
		}
		question := request.Question[0].Name
		select {
		case s.received <- question:
		default:
		}
		if !s.shouldAnswer(addr, question) {
			continue
		}
		response := new(mDNS.Msg)
		response.SetReply(&request)
		response.Answer = []mDNS.RR{&mDNS.A{
			Hdr: mDNS.RR_Header{Name: question, Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60},
			A:   net.IPv4(127, 0, 0, 1),
		}}
		raw, err := response.Pack()
		if err != nil {
			continue
		}
		s.conn.WriteToUDP(raw, addr)
	}
}

// awaitReceived blocks until the server has read a datagram carrying question.
// Used instead of sleeps so ordering assertions are deterministic: receipt
// proves the query was already written, hence its read epoch already captured.
func (s *fakeUpstream) awaitReceived(t *testing.T, question string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-s.received:
			if got == question {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the upstream to receive %q", question)
		}
	}
}

// gatedConn lets a test freeze a socket at the moment of Write, so the window
// between acquiring a pooled connection and writing to it — normally
// unobservable — can be driven deterministically instead of raced.
type gatedConn struct {
	net.Conn

	access sync.Mutex
	armed  bool
	paused chan struct{}
	resume chan struct{}
}

// armWrite makes the NEXT Write on this socket block until resumeWrite is
// called, after which the write proceeds against whatever state the socket is
// then in. One-shot.
func (c *gatedConn) armWrite() {
	c.access.Lock()
	defer c.access.Unlock()
	c.armed = true
}

func (c *gatedConn) resumeWrite() { close(c.resume) }

func (c *gatedConn) Write(b []byte) (int, error) {
	c.access.Lock()
	armed := c.armed
	c.armed = false
	c.access.Unlock()
	if armed {
		close(c.paused)
		<-c.resume
	}
	return c.Conn.Write(b)
}

// countingDialer dials the fake upstream over a real loopback UDP socket,
// records how many sockets were created, and declares each one to the server as
// a fresh client generation.
type countingDialer struct {
	server *fakeUpstream
	dials  atomic.Int32

	access sync.Mutex
	conns  []*gatedConn
}

func (d *countingDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := new(net.Dialer).DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	gated := &gatedConn{
		Conn:   conn,
		paused: make(chan struct{}),
		resume: make(chan struct{}),
	}
	d.access.Lock()
	d.conns = append(d.conns, gated)
	d.access.Unlock()
	d.dials.Add(1)
	d.server.registerClient(conn.LocalAddr())
	return gated, nil
}

// conn returns the index-th socket this dialer created.
func (d *countingDialer) conn(t *testing.T, index int) *gatedConn {
	t.Helper()
	d.access.Lock()
	defer d.access.Unlock()
	if index >= len(d.conns) {
		t.Fatalf("socket %d was never dialed (have %d)", index, len(d.conns))
	}
	return d.conns[index]
}

func (d *countingDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp", "")
}

func newTestTransport(t *testing.T, server *fakeUpstream) (*UDPTransport, *countingDialer) {
	t.Helper()
	dialerInstance := &countingDialer{server: server}
	transport := NewUDPRaw(
		logger.NOP(),
		dns.NewTransportAdapter(C.DNSTypeUDP, "test-udp", nil),
		dialerInstance,
		M.SocksaddrFromNet(server.conn.LocalAddr()),
	)
	t.Cleanup(func() { transport.Close() })
	return transport, dialerInstance
}

func query(name string) *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion(name, mDNS.TypeA)
	return message
}

func exchangeWithTimeout(t *testing.T, transport *UDPTransport, name string, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := transport.Exchange(ctx, query(name))
	return err
}

// TestUDPTransportInvalidatesSilentlyDeadConn is the regression gate for the
// stale pooled UDP DNS socket (ticket 18766). With the invalidate-on-quiet-
// deadline patch reverted the third query reuses the dead socket and times out,
// so this test fails.
func TestUDPTransportInvalidatesSilentlyDeadConn(t *testing.T) {
	server := newFakeUpstream(t, true)
	transport, dialerInstance := newTestTransport(t, server)

	// Query 1: fresh socket, upstream answers.
	if err := exchangeWithTimeout(t, transport, "first.example.com.", 2*time.Second); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if dials := dialerInstance.dials.Load(); dials != 1 {
		t.Fatalf("after first exchange: dials = %d, want 1", dials)
	}

	// Query 2: same socket, upstream now silent — must hit the deadline.
	err := exchangeWithTimeout(t, transport, "second.example.com.", 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second exchange: err = %v, want context.DeadlineExceeded", err)
	}

	// Query 3: the dead socket must have been dropped from the pool, so this
	// redials and succeeds. Without the fix the pooled socket is reused and
	// this times out.
	if err := exchangeWithTimeout(t, transport, "third.example.com.", 2*time.Second); err != nil {
		t.Fatalf("third exchange after dead socket: %v", err)
	}
	if dials := dialerInstance.dials.Load(); dials != 2 {
		t.Fatalf("after third exchange: dials = %d, want 2 (dead socket must be redialed)", dials)
	}
}

// TestUDPTransportKeepsConnWhenSiblingQueriesSucceed covers the epoch's whole
// point: one query timing out proves nothing if OTHER queries kept getting
// answers on the same socket. The socket is demonstrably alive, so it must stay
// pooled and no redial may happen.
func TestUDPTransportKeepsConnWhenSiblingQueriesSucceed(t *testing.T) {
	const quiet = "quiet.example.com."
	server := newFakeUpstream(t, false, quiet)
	transport, dialerInstance := newTestTransport(t, server)

	// Establish the shared socket.
	if err := exchangeWithTimeout(t, transport, "warmup.example.com.", 2*time.Second); err != nil {
		t.Fatalf("warmup exchange: %v", err)
	}

	quietErr := make(chan error, 1)
	go func() {
		quietErr <- exchangeWithTimeout(t, transport, quiet, time.Second)
	}()

	// Receipt proves the quiet query is written and its epoch captured, so the
	// live responses below are guaranteed to land AFTER the capture.
	server.awaitReceived(t, quiet)

	// Keep answering other queries on the same socket while the quiet one is
	// still waiting. Each success bumps that socket's read epoch.
	for i := 0; i < 3; i++ {
		if err := exchangeWithTimeout(t, transport, "live.example.com.", 2*time.Second); err != nil {
			t.Fatalf("concurrent live exchange %d: %v", i, err)
		}
	}

	if err := <-quietErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiet exchange: err = %v, want context.DeadlineExceeded", err)
	}

	// The socket answered other queries throughout, so it must have survived.
	if err := exchangeWithTimeout(t, transport, "after.example.com.", 2*time.Second); err != nil {
		t.Fatalf("exchange after quiet timeout: %v", err)
	}
	if dials := dialerInstance.dials.Load(); dials != 1 {
		t.Fatalf("a live socket must not be evicted: dials = %d, want 1", dials)
	}
}

// TestUDPTransportSiblingSeesDistinctInvalidationCause locks the error-meaning
// contract: the query whose deadline evicts the shared socket keeps its own
// context.DeadlineExceeded, while a sibling still in flight — whose own context
// is nowhere near expiry — observes the distinct invalidation cause rather than
// a borrowed DeadlineExceeded.
func TestUDPTransportSiblingSeesDistinctInvalidationCause(t *testing.T) {
	const sibling = "sibling.example.com."
	const trigger = "trigger.example.com."
	// Both names are dropped, so no read ever lands: the epoch stays put and the
	// trigger's deadline is guaranteed to evict.
	server := newFakeUpstream(t, false, sibling, trigger)
	transport, _ := newTestTransport(t, server)

	siblingErr := make(chan error, 1)
	go func() {
		siblingErr <- exchangeWithTimeout(t, transport, sibling, 10*time.Second)
	}()
	server.awaitReceived(t, sibling)

	err := exchangeWithTimeout(t, transport, trigger, 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("triggering exchange: err = %v, want its own context.DeadlineExceeded", err)
	}
	if errors.Is(err, errUDPSharedConnInvalidated) {
		t.Fatalf("triggering exchange must not report the sibling cause: %v", err)
	}

	select {
	case err := <-siblingErr:
		if !errors.Is(err, errUDPSharedConnInvalidated) {
			t.Fatalf("sibling exchange: err = %v, want errUDPSharedConnInvalidated", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sibling must not inherit a peer's deadline: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sibling exchange did not return after the shared socket was invalidated")
	}

	// Exactly once: the channel is buffered for one send and the goroutine has
	// exited, so nothing further may arrive.
	select {
	case extra := <-siblingErr:
		t.Fatalf("sibling exchange returned twice: %v", extra)
	default:
	}
}

// TestUDPTransportRetriesWriteOnReusedConn covers the window between acquiring
// a POOLED socket and writing to it. If a sibling evicts that socket in between,
// the write fails for a reason that has nothing to do with this query and the
// request never reached the wire — so the query must redial and retry once
// rather than surfacing a stale peer's error. Exactly one retry, and only for a
// socket we did not dial ourselves (upstream 1.14's `!created && firstAttempt`).
func TestUDPTransportRetriesWriteOnReusedConn(t *testing.T) {
	const evictor = "evictor.example.com."
	server := newFakeUpstream(t, false, evictor)
	transport, dialerInstance := newTestTransport(t, server)

	// Establish the shared socket.
	if err := exchangeWithTimeout(t, transport, "warmup.example.com.", 2*time.Second); err != nil {
		t.Fatalf("warmup exchange: %v", err)
	}
	connA := dialerInstance.conn(t, 0)

	// Freeze the next write on the pooled socket, then launch the query that
	// will hit that freeze. It has acquired connA as a REUSED conn.
	connA.armWrite()
	retryErr := make(chan error, 1)
	go func() {
		retryErr <- exchangeWithTimeout(t, transport, "retry.example.com.", 5*time.Second)
	}()
	<-connA.paused

	// A sibling now evicts connA the ordinary way: its answer never comes, the
	// socket's read epoch never moves, its deadline fires.
	if err := exchangeWithTimeout(t, transport, evictor, 300*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("evicting exchange: err = %v, want context.DeadlineExceeded", err)
	}

	// Unfreeze: the write now lands on a closed socket and must be retried on a
	// freshly dialed one instead of failing the caller.
	connA.resumeWrite()

	select {
	case err := <-retryErr:
		if err != nil {
			t.Fatalf("query on an evicted pooled socket must redial, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("query never returned after the pooled socket was evicted mid-write")
	}
	if dials := dialerInstance.dials.Load(); dials != 2 {
		t.Fatalf("dials = %d, want 2 (one retry against a freshly dialed socket)", dials)
	}
}

// TestUDPTransportKeepsConnOnCancel locks the other half of the contract: plain
// cancellation says nothing about the socket, so it stays pooled.
func TestUDPTransportKeepsConnOnCancel(t *testing.T) {
	const cancelled = "cancelled.example.com."
	server := newFakeUpstream(t, false, cancelled)
	transport, dialerInstance := newTestTransport(t, server)

	if err := exchangeWithTimeout(t, transport, "warmup.example.com.", 2*time.Second); err != nil {
		t.Fatalf("warmup exchange: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelErr := make(chan error, 1)
	go func() {
		_, err := transport.Exchange(ctx, query(cancelled))
		cancelErr <- err
	}()
	// Cancel only once the query is demonstrably on the wire — no sleeps.
	server.awaitReceived(t, cancelled)
	cancel()

	if err := <-cancelErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled exchange: err = %v, want context.Canceled", err)
	}

	// The socket was never proven dead, so it must still be pooled: the next
	// query reuses it rather than redialing.
	if err := exchangeWithTimeout(t, transport, "after.example.com.", 2*time.Second); err != nil {
		t.Fatalf("exchange after cancellation: %v", err)
	}
	if dials := dialerInstance.dials.Load(); dials != 1 {
		t.Fatalf("cancellation must not drop the pooled socket: dials = %d, want 1", dials)
	}
}
