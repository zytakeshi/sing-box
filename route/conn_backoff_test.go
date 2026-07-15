package route

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// --- test doubles -----------------------------------------------------------

var errFakeDial = E.New("fake dial failure")

// captureLogger records WarnContext lines; all other methods are NOP.
type captureLogger struct {
	logger.ContextLogger
	mu    sync.Mutex
	warns []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{ContextLogger: logger.NOP()}
}

func (l *captureLogger) WarnContext(ctx context.Context, args ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, fmt.Sprint(args...))
	l.mu.Unlock()
}

func (l *captureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.warns))
	copy(out, l.warns)
	return out
}

// fakeConn is a closable stand-in for a dialed remote/inbound connection.
type fakeConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *fakeConn) Close() error {
	c.closed.Store(true)
	return nil
}

// mockOutbound implements adapter.Outbound for gate-key and NewConnection tests.
type mockOutbound struct {
	typ  string
	tag  string
	err  error
	conn net.Conn
}

func (o mockOutbound) Type() string           { return o.typ }
func (o mockOutbound) Tag() string            { return o.tag }
func (o mockOutbound) Network() []string      { return nil }
func (o mockOutbound) Dependencies() []string { return nil }
func (o mockOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.conn != nil {
		return o.conn, nil
	}
	return &fakeConn{}, nil
}
func (o mockOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}

// plainDialer implements only N.Dialer (not adapter.Outbound).
type plainDialer struct{}

func (plainDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (plainDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}

// --- helpers ----------------------------------------------------------------

func newTestBackoff() (*backoffController, *captureLogger) {
	lg := newCaptureLogger()
	c := newBackoffController(lg)
	c.baseDelay = 10 * time.Millisecond
	c.maxDelay = 40 * time.Millisecond
	c.parkTimeout = 60 * time.Millisecond
	return c, lg
}

func failDial(ctx context.Context) (net.Conn, error) { return nil, errFakeDial }
func okDial(ctx context.Context) (net.Conn, error)   { return &fakeConn{}, nil }

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func countWith(lines []string, substr string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// trip drives exactly tripAt failing dials to trip the gate for tag.
func trip(c *backoffController, tag string) *leafGate {
	for i := 0; i < c.tripAt; i++ {
		c.runDial(context.Background(), tag, failDial)
	}
	return c.gates[tag]
}

func slotLen(c *backoffController, gate *leafGate) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(gate.slots)
}

// --- tests ------------------------------------------------------------------

func TestBackoffPacingDelayCurve(t *testing.T) {
	c := newBackoffController(logger.NOP())
	cases := map[int]time.Duration{
		0:   250 * time.Millisecond,
		4:   250 * time.Millisecond,
		5:   250 * time.Millisecond, // shift 0
		6:   500 * time.Millisecond, // shift 1
		7:   1 * time.Second,        // shift 2
		8:   2 * time.Second,        // shift 3
		9:   2 * time.Second,        // shift clamped + capped
		100: 2 * time.Second,
	}
	for consecutive, want := range cases {
		if got := c.pacingDelay(consecutive); got != want {
			t.Fatalf("pacingDelay(%d) = %s, want %s", consecutive, got, want)
		}
	}
}

func TestBackoffTripAndRecover(t *testing.T) {
	c, lg := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"

	for i := 0; i < 4; i++ {
		if _, _, proceed := c.runDial(ctx, tag, failDial); !proceed {
			t.Fatalf("attempt %d should have proceeded to dial", i)
		}
	}
	gate := c.gates[tag]
	if gate.tripped {
		t.Fatal("gate tripped after only 4 failures")
	}
	if _, _, proceed := c.runDial(ctx, tag, failDial); !proceed {
		t.Fatal("5th attempt should have proceeded to dial")
	}
	if !gate.tripped {
		t.Fatal("gate not tripped after 5 consecutive failures")
	}
	if n := countWith(lg.snapshot(), "tripped tag=proxy consecutive=5"); n != 1 {
		t.Fatalf("want exactly 1 tripped log, got %d (%v)", n, lg.snapshot())
	}

	conn, err, proceed := c.runDial(ctx, tag, okDial)
	if !proceed || err != nil {
		t.Fatalf("success dial: proceed=%v err=%v", proceed, err)
	}
	if conn != nil {
		conn.Close()
	}
	if gate.tripped {
		t.Fatal("gate still tripped after a success")
	}
	if gate.consecutive != 0 {
		t.Fatalf("consecutive not reset, got %d", gate.consecutive)
	}
	if n := countWith(lg.snapshot(), "recovered tag=proxy"); n != 1 {
		t.Fatalf("want exactly 1 recovered log, got %d (%v)", n, lg.snapshot())
	}
	if got := countWith(lg.snapshot(), "connection_failure_backoff:"); got != 2 {
		t.Fatalf("want exactly 2 backoff log lines total, got %d (%v)", got, lg.snapshot())
	}
}

func TestBackoffGapResets(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"
	fake := time.Unix(0, 0)
	c.now = func() time.Time { return fake }

	for i := 0; i < 5; i++ {
		c.runDial(ctx, tag, failDial)
	}
	gate := c.gates[tag]
	if !gate.tripped {
		t.Fatal("expected tripped")
	}
	fake = fake.Add(c.resetGap + time.Second)
	c.runDial(ctx, tag, failDial)
	if gate.tripped {
		t.Fatal("gate should have recovered after a >=resetGap gap")
	}
	if gate.consecutive != 1 {
		t.Fatalf("consecutive should be 1 after gap reset, got %d", gate.consecutive)
	}
}

// Success-generation suppression exercised through OVERLAPPING runDial calls:
// attempt A stamps its generation, a concurrent success bumps the generation,
// then A's late failure must not re-poison the gate.
func TestBackoffSuccessGenerationSuppressionOverlap(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"

	aStamped := make(chan struct{})
	aRelease := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		c.runDial(ctx, tag, func(dialCtx context.Context) (net.Conn, error) {
			close(aStamped) // fires after the generation is stamped under the lock
			<-aRelease
			return nil, errFakeDial
		})
	}()
	<-aStamped

	// A concurrent success bumps successGeneration.
	conn, err, proceed := c.runDial(ctx, tag, okDial)
	if !proceed || err != nil {
		t.Fatalf("overlapping success: proceed=%v err=%v", proceed, err)
	}
	if conn != nil {
		conn.Close()
	}

	close(aRelease) // A now fails, but from a stale generation
	<-aDone

	c.mu.Lock()
	gate := c.gates[tag]
	consecutive := gate.consecutive
	c.mu.Unlock()
	if consecutive != 0 {
		t.Fatalf("late stale-generation failure must be suppressed, consecutive=%d", consecutive)
	}
}

func TestBackoffDirectAndNonOutboundBypass(t *testing.T) {
	if _, ok := backoffTag(mockOutbound{typ: C.TypeDirect, tag: "direct-out"}); ok {
		t.Fatal("direct-type outbound must bypass the gate")
	}
	tag, ok := backoffTag(mockOutbound{typ: "vmess", tag: "proxy-a"})
	if !ok || tag != "proxy-a" {
		t.Fatalf("proxy outbound should be gated with its tag, got tag=%q ok=%v", tag, ok)
	}
	if _, ok := backoffTag(plainDialer{}); ok {
		t.Fatal("non-Outbound dialer must bypass the gate")
	}
}

// Direct bypass asserted through the REAL NewConnection dial path.
func TestBackoffDirectBypassThroughNewConnection(t *testing.T) {
	m := NewConnectionManager(logger.NOP(), true)
	metadata := adapter.InboundContext{Destination: M.ParseSocksaddr("example.com:80")}

	directOut := mockOutbound{typ: C.TypeDirect, tag: "direct", err: errFakeDial}
	m.NewConnection(context.Background(), directOut, &fakeConn{}, metadata, func(error) {})
	m.backoff.mu.Lock()
	nGates := len(m.backoff.gates)
	m.backoff.mu.Unlock()
	if nGates != 0 {
		t.Fatalf("direct outbound must not create a gate, got %d", nGates)
	}

	proxyOut := mockOutbound{typ: "vmess", tag: "proxy", err: errFakeDial}
	m.NewConnection(context.Background(), proxyOut, &fakeConn{}, metadata, func(error) {})
	m.backoff.mu.Lock()
	gate := m.backoff.gates["proxy"]
	m.backoff.mu.Unlock()
	if gate == nil || gate.consecutive != 1 {
		t.Fatalf("proxy outbound should record one failure through the gate, gate=%v", gate)
	}
}

// Disabled path: no controller, dial still runs stock.
func TestBackoffDisabledPathNoController(t *testing.T) {
	m := NewConnectionManager(logger.NOP(), false)
	if m.backoff != nil {
		t.Fatal("backoff controller must be nil when disabled")
	}
	metadata := adapter.InboundContext{Destination: M.ParseSocksaddr("example.com:80")}
	// Should not panic and must take the stock error path.
	m.NewConnection(context.Background(), mockOutbound{typ: "vmess", tag: "proxy", err: errFakeDial}, &fakeConn{}, metadata, func(error) {})
	m.BeginClose() // no-op when disabled
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestBackoffPerTagIsolation(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	trip(c, "A")
	if !c.gates["A"].tripped {
		t.Fatal("tag A should be tripped")
	}
	start := time.Now()
	conn, err, proceed := c.runDial(ctx, "B", okDial)
	if !proceed || err != nil {
		t.Fatalf("tag B dial: proceed=%v err=%v", proceed, err)
	}
	if conn != nil {
		conn.Close()
	}
	if c.gates["B"].tripped {
		t.Fatal("tag B must not be affected by tag A")
	}
	if elapsed := time.Since(start); elapsed >= c.baseDelay {
		t.Fatalf("tag B dial should not be paced, took %s", elapsed)
	}
}

func TestBackoffPendingCapFromAttemptOne(t *testing.T) {
	c, _ := newTestBackoff()
	c.maxPending = 2
	c.parkTimeout = 50 * time.Millisecond
	ctx := context.Background()
	tag := "proxy"

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.runDial(ctx, tag, func(context.Context) (net.Conn, error) {
				<-release
				return nil, errFakeDial
			})
		}()
	}
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		gate := c.gates[tag]
		c.mu.Unlock()
		return gate != nil && slotLen(c, gate) == 0
	})

	start := time.Now()
	_, err, proceed := c.runDial(ctx, tag, okDial)
	if proceed {
		t.Fatal("third attempt should have overflowed, not dialed")
	}
	if err != errBackoffOverflow {
		t.Fatalf("want overflow error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < c.parkTimeout/2 {
		t.Fatalf("overflow returned too fast (%s), did not park", elapsed)
	}
	close(release)
	wg.Wait()
}

// A slot-PARKED attempt (waiting for a token, not yet dialing) whose ctx is
// canceled must never invoke dialFunc, must return the caller cause (not
// overflow), and must not consume/leak a slot.
func TestBackoffSlotParkCancelNeverDials(t *testing.T) {
	c, _ := newTestBackoff()
	c.maxPending = 2
	c.parkTimeout = 2 * time.Second
	ctx := context.Background()
	tag := "proxy"

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < c.maxPending; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.runDial(ctx, tag, func(context.Context) (net.Conn, error) {
				<-release
				return nil, errFakeDial
			})
		}()
	}
	var gate *leafGate
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		gate = c.gates[tag]
		c.mu.Unlock()
		return gate != nil && slotLen(c, gate) == 0
	})

	var dialed atomic.Bool
	cctx, cancel := context.WithCancel(ctx)
	res := make(chan error, 1)
	go func() {
		_, err, _ := c.runDial(cctx, tag, func(context.Context) (net.Conn, error) {
			dialed.Store(true)
			return nil, errFakeDial
		})
		res <- err
	}()
	time.Sleep(20 * time.Millisecond) // ensure it is parked on the empty slot channel
	cancel()

	select {
	case err := <-res:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("parked-then-canceled attempt should return the caller cause, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked attempt did not return after ctx cancel")
	}
	if dialed.Load() {
		t.Fatal("parked attempt must not have invoked dialFunc")
	}
	if got := c.gates[tag].overflowSinceTrip; got != 0 {
		t.Fatalf("cancel must not be counted as overflow, got %d", got)
	}

	close(release)
	wg.Wait()
	eventually(t, time.Second, func() bool { return slotLen(c, gate) == c.maxPending })
}

// A pre-canceled ctx with an immediately available slot must not dial.
func TestBackoffCanceledCtxWithAvailableSlotNeverDials(t *testing.T) {
	c, _ := newTestBackoff()
	tag := "proxy"
	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	var dialed atomic.Bool
	_, err, proceed := c.runDial(cctx, tag, func(context.Context) (net.Conn, error) {
		dialed.Store(true)
		return okDial(context.Background())
	})
	if proceed {
		t.Fatal("canceled attempt must not proceed")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled cause, got %v", err)
	}
	if dialed.Load() {
		t.Fatal("dialFunc must not run for a pre-canceled attempt")
	}
}

// A canceled parked attempt reports the caller cause, and only a genuine
// timeout increments overflow telemetry.
func TestBackoffAbortCausesAreDistinct(t *testing.T) {
	c, _ := newTestBackoff()
	c.maxPending = 1
	c.parkTimeout = 40 * time.Millisecond
	tag := "proxy"

	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.runDial(context.Background(), tag, func(context.Context) (net.Conn, error) {
			<-release
			return nil, errFakeDial
		})
	}()
	var gate *leafGate
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		gate = c.gates[tag]
		c.mu.Unlock()
		return gate != nil && slotLen(c, gate) == 0
	})

	// Canceled parker → caller cause, not overflow.
	cctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		_, err, _ := c.runDial(cctx, tag, okDial)
		res <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parker cause = %v, want context.Canceled", err)
	}
	if gate.overflowSinceTrip != 0 {
		t.Fatalf("cancel counted as overflow: %d", gate.overflowSinceTrip)
	}

	// Genuine timeout → overflow, counted.
	_, err, proceed := c.runDial(context.Background(), tag, okDial)
	if proceed || err != errBackoffOverflow {
		t.Fatalf("timeout parker: proceed=%v err=%v", proceed, err)
	}
	if gate.overflowSinceTrip != 1 {
		t.Fatalf("timeout should increment overflow once, got %d", gate.overflowSinceTrip)
	}

	close(release)
	<-done
}

// A paced (slot-held) attempt canceled during its pacing sleep aborts and
// releases its slot.
func TestBackoffPacedCtxCancelReleasesSlot(t *testing.T) {
	c, _ := newTestBackoff()
	c.maxPending = 4
	c.baseDelay = 500 * time.Millisecond
	c.maxDelay = 500 * time.Millisecond
	ctx := context.Background()
	tag := "proxy"

	gate := trip(c, tag)
	if !gate.tripped {
		t.Fatal("expected tripped gate")
	}

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan bool, 1)
	go func() {
		_, _, proceed := c.runDial(cctx, tag, okDial)
		done <- proceed
	}()
	eventually(t, time.Second, func() bool { return slotLen(c, gate) == c.maxPending-1 })
	cancel()

	select {
	case proceed := <-done:
		if proceed {
			t.Fatal("canceled paced attempt should not proceed")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled paced attempt did not return")
	}
	eventually(t, time.Second, func() bool { return slotLen(c, gate) == c.maxPending })
}

// Deterministic FIFO: waiters enqueued in a known order are granted in that
// order.
func TestBackoffPermitFifoDeterministicOrder(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 15 * time.Millisecond
	c.maxDelay = 15 * time.Millisecond
	tag := "proxy"

	c.mu.Lock()
	gate := newLeafGate(c.maxPending)
	gate.consecutive = c.tripAt
	gate.tripped = true
	gate.permitFlush = make(chan struct{})
	c.gates[tag] = gate
	epoch := c.epoch
	c.mu.Unlock()

	const K = 5
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < K; i++ {
		wg.Add(1)
		want := i + 1
		go func(i int) {
			defer wg.Done()
			if c.waitPermit(context.Background(), epoch, gate) == nil {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
			}
		}(i)
		// Enqueue strictly in order: wait until this waiter is registered.
		eventually(t, time.Second, func() bool {
			c.mu.Lock()
			n := len(gate.permitWaiters)
			c.mu.Unlock()
			return n == want
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != K {
		t.Fatalf("want %d grants, got %d", K, len(order))
	}
	for i := 0; i < K; i++ {
		if order[i] != i {
			t.Fatalf("FIFO order violated: %v", order)
		}
	}
}

// FIFO spacing plus canceled-ticket reclaim (no stall / no schedule debt).
func TestBackoffPermitFifoSpacingAndReclaim(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 30 * time.Millisecond
	c.maxDelay = 30 * time.Millisecond
	tag := "proxy"

	c.mu.Lock()
	gate := newLeafGate(c.maxPending)
	gate.consecutive = c.tripAt
	gate.tripped = true
	gate.permitFlush = make(chan struct{})
	c.gates[tag] = gate
	epoch := c.epoch
	c.mu.Unlock()

	const K = 6
	var mu sync.Mutex
	var grants []time.Time
	proceeded := int32(0)
	var wg sync.WaitGroup
	cancelIdx := 2
	ctxs := make([]context.Context, K)
	cancels := make([]context.CancelFunc, K)
	for i := 0; i < K; i++ {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
	}
	for i := 0; i < K; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if c.waitPermit(ctxs[i], epoch, gate) == nil {
				atomic.AddInt32(&proceeded, 1)
				mu.Lock()
				grants = append(grants, time.Now())
				mu.Unlock()
			}
		}(i)
	}
	time.Sleep(2 * time.Millisecond) // well before the first grant
	cancels[cancelIdx]()

	wg.Wait()
	for _, cancel := range cancels {
		cancel()
	}
	if got := atomic.LoadInt32(&proceeded); got != K-1 {
		t.Fatalf("want %d paced grants (one canceled), got %d", K-1, got)
	}
	mu.Lock()
	sort.Slice(grants, func(a, b int) bool { return grants[a].Before(grants[b]) })
	local := append([]time.Time(nil), grants...)
	mu.Unlock()
	minGap := c.baseDelay / 2
	for i := 1; i < len(local); i++ {
		if gap := local[i].Sub(local[i-1]); gap < minGap {
			t.Fatalf("grants not serialized: gap %s < %s", gap, minGap)
		}
	}
}

// A canceled HEAD (in-flight baton) hands off to the next waiter.
func TestBackoffCanceledHeadHandsOffBaton(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 200 * time.Millisecond
	c.maxDelay = 200 * time.Millisecond
	tag := "proxy"

	c.mu.Lock()
	gate := newLeafGate(c.maxPending)
	gate.consecutive = c.tripAt
	gate.tripped = true
	gate.permitFlush = make(chan struct{})
	c.gates[tag] = gate
	epoch := c.epoch
	c.mu.Unlock()

	headCtx, cancelHead := context.WithCancel(context.Background())
	headRes := make(chan error, 1)
	go func() { headRes <- c.waitPermit(headCtx, epoch, gate) }()
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		n := len(gate.permitWaiters)
		c.mu.Unlock()
		return n == 1
	})
	nextRes := make(chan error, 1)
	go func() { nextRes <- c.waitPermit(context.Background(), epoch, gate) }()
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		n := len(gate.permitWaiters)
		c.mu.Unlock()
		return n == 2
	})

	cancelHead() // head aborts mid-sleep, must promote the next waiter
	if err := <-headRes; !errors.Is(err, context.Canceled) {
		t.Fatalf("head cause = %v, want context.Canceled", err)
	}
	select {
	case err := <-nextRes:
		if err != nil {
			t.Fatalf("promoted waiter should grant, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("baton not handed off to next waiter after head cancel")
	}
}

// Queue drains to empty while a grant is in flight; a fresh waiter then
// promotes itself.
func TestBackoffQueueEmptyThenNewWaiter(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 10 * time.Millisecond
	c.maxDelay = 10 * time.Millisecond
	tag := "proxy"

	c.mu.Lock()
	gate := newLeafGate(c.maxPending)
	gate.consecutive = c.tripAt
	gate.tripped = true
	gate.permitFlush = make(chan struct{})
	c.gates[tag] = gate
	epoch := c.epoch
	c.mu.Unlock()

	if err := c.waitPermit(context.Background(), epoch, gate); err != nil {
		t.Fatalf("first waiter should grant, got %v", err)
	}
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		n := len(gate.permitWaiters)
		c.mu.Unlock()
		return n == 0
	})
	if err := c.waitPermit(context.Background(), epoch, gate); err != nil {
		t.Fatalf("fresh waiter after empty queue should grant, got %v", err)
	}
}

func TestBackoffEpochAbortsSleeper(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 2 * time.Second
	c.maxDelay = 2 * time.Second
	tag := "proxy"

	c.mu.Lock()
	gate := newLeafGate(c.maxPending)
	gate.consecutive = c.tripAt
	gate.tripped = true
	gate.permitFlush = make(chan struct{})
	c.gates[tag] = gate
	epoch := c.epoch
	c.mu.Unlock()

	res := make(chan error, 1)
	go func() { res <- c.waitPermit(context.Background(), epoch, gate) }()
	time.Sleep(20 * time.Millisecond)
	c.CloseAll()

	select {
	case err := <-res:
		if err != errBackoffReset {
			t.Fatalf("sleeper should abort with reset cause, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sleeper did not abort promptly on epoch rotation")
	}
}

func TestBackoffEpochAbortsInflightDial(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"

	started := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		_, _, proceed := c.runDial(ctx, tag, func(dialCtx context.Context) (net.Conn, error) {
			close(started)
			<-dialCtx.Done()
			return nil, dialCtx.Err()
		})
		done <- proceed
	}()
	<-started
	c.CloseAll()

	select {
	case proceed := <-done:
		if proceed {
			t.Fatal("aborted in-flight dial should return proceed=false")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight dial not aborted by epoch cancellation")
	}
	c.mu.Lock()
	_, exists := c.gates[tag]
	c.mu.Unlock()
	if exists {
		t.Fatal("gate map should have been cleared by CloseAll")
	}
}

func TestBackoffStaleResultSuppressed(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"

	started := make(chan struct{})
	release := make(chan struct{})
	conn := &fakeConn{}
	done := make(chan bool, 1)
	go func() {
		_, _, proceed := c.runDial(ctx, tag, func(dialCtx context.Context) (net.Conn, error) {
			close(started)
			<-release
			return conn, nil
		})
		done <- proceed
	}()
	<-started
	c.CloseAll()
	close(release)

	select {
	case proceed := <-done:
		if proceed {
			t.Fatal("stale-epoch result must be suppressed (proceed=false)")
		}
	case <-time.After(time.Second):
		t.Fatal("stale dial did not return")
	}
	if !conn.closed.Load() {
		t.Fatal("stale remote connection must be closed, not retained")
	}
}

func TestBackoffBeginCloseRejectsAndAborts(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 2 * time.Second
	c.maxDelay = 2 * time.Second
	ctx := context.Background()
	tag := "proxy"

	gate := trip(c, tag)
	done := make(chan bool, 1)
	go func() {
		_, _, proceed := c.runDial(ctx, tag, okDial)
		done <- proceed
	}()
	eventually(t, time.Second, func() bool { return slotLen(c, gate) == c.maxPending-1 })

	c.BeginClose()

	select {
	case proceed := <-done:
		if proceed {
			t.Fatal("sleeper should abort on BeginClose")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sleeper not aborted by BeginClose")
	}
	if _, err, proceed := c.runDial(ctx, tag, okDial); proceed || err != errBackoffClosing {
		t.Fatalf("post-BeginClose admission: proceed=%v err=%v", proceed, err)
	}
	c.Close()
}

// BeginClose fired right at the pacing-timer boundary must not panic/deadlock;
// the attempt returns and Close drains. -race -count exercises the boundary.
func TestBackoffBeginCloseAtPermitTimerBoundary(t *testing.T) {
	c, _ := newTestBackoff()
	c.baseDelay = 20 * time.Millisecond
	c.maxDelay = 20 * time.Millisecond
	ctx := context.Background()
	tag := "proxy"

	trip(c, tag)
	done := make(chan struct{})
	go func() {
		c.runDial(ctx, tag, okDial)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // right around the timer boundary
	c.BeginClose()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attempt did not return around the timer boundary")
	}
	c.Close()
}

func TestBackoffCloseWaitsForWorkers(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	tag := "proxy"

	started := make(chan struct{})
	go func() {
		c.runDial(ctx, tag, func(dialCtx context.Context) (net.Conn, error) {
			close(started)
			<-dialCtx.Done()
			return nil, dialCtx.Err()
		})
	}()
	<-started

	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not drain in-flight worker")
	}
}

// Close racing continuous admissions: no WaitGroup Add-after-Wait panic.
func TestBackoffCloseRacesAdmissions(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tag := fmt.Sprintf("proxy-%d", i%4)
			for j := 0; j < 40; j++ {
				conn, err, proceed := c.runDial(ctx, tag, okDial)
				if proceed && conn != nil {
					conn.Close()
				}
				if err == errBackoffClosing {
					return
				}
			}
		}(i)
	}
	time.Sleep(2 * time.Millisecond)
	c.Close()
	wg.Wait()
}

// Admission vs CloseAll with a post-race liveness invariant.
func TestBackoffAdmissionVsCloseAll(t *testing.T) {
	c, _ := newTestBackoff()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tag := fmt.Sprintf("proxy-%d", i%4)
			if i%2 == 0 {
				c.runDial(ctx, tag, failDial)
			} else {
				conn, _, proceed := c.runDial(ctx, tag, okDial)
				if proceed && conn != nil {
					conn.Close()
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.CloseAll()
		}()
	}
	wg.Wait()

	// Post-race invariant: the controller is still live and functional.
	c.CloseAll()
	conn, err, proceed := c.runDial(ctx, "post", okDial)
	if !proceed || err != nil {
		t.Fatalf("controller not functional after storm: proceed=%v err=%v", proceed, err)
	}
	if conn != nil {
		conn.Close()
	}
	c.Close()
}

// Synchronous barrier: an attempt held just before dial, then BeginClose run to
// completion, must find its dial context already canceled — dialFunc observes
// ctx.Err() != nil AT ENTRY (or never runs), with no history/log.
func TestBackoffBeginCloseBarrierCancelsBeforeDial(t *testing.T) {
	c, lg := newTestBackoff()
	tag := "proxy"

	proceed := make(chan struct{})
	c.beforeDial = func() { <-proceed } // hold after enrollment, before dial

	var dialRan atomic.Bool
	var entryErr error
	res := make(chan struct{})
	go func() {
		defer close(res)
		c.runDial(context.Background(), tag, func(dctx context.Context) (net.Conn, error) {
			dialRan.Store(true)
			entryErr = dctx.Err()
			return nil, errFakeDial
		})
	}()

	// Worker is enrolled and parked just before the dial.
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		n := len(c.inFlight)
		c.mu.Unlock()
		return n == 1
	})

	c.BeginClose() // barrier to completion

	c.mu.Lock()
	nAfter := len(c.inFlight)
	c.mu.Unlock()
	if nAfter != 0 {
		t.Fatalf("barrier did not clear the in-flight registry, %d left", nAfter)
	}

	close(proceed) // release into dialFunc
	<-res

	if dialRan.Load() && entryErr == nil {
		t.Fatal("dialFunc ran with a live context after the barrier")
	}
	c.mu.Lock()
	gate := c.gates[tag]
	c.mu.Unlock()
	if gate != nil && gate.consecutive != 0 {
		t.Fatalf("post-barrier dial polluted failure history: consecutive=%d", gate.consecutive)
	}
	if lines := lg.snapshot(); len(lines) != 0 {
		t.Fatalf("post-barrier dial produced logs: %v", lines)
	}
	c.Close()
}

// Same barrier guarantee for the CloseAll (epoch-rotation) path.
func TestBackoffCloseAllBarrierCancelsBeforeDial(t *testing.T) {
	c, lg := newTestBackoff()
	tag := "proxy"

	proceed := make(chan struct{})
	c.beforeDial = func() { <-proceed }

	var dialRan atomic.Bool
	var entryErr error
	res := make(chan struct{})
	go func() {
		defer close(res)
		c.runDial(context.Background(), tag, func(dctx context.Context) (net.Conn, error) {
			dialRan.Store(true)
			entryErr = dctx.Err()
			return nil, errFakeDial
		})
	}()
	eventually(t, time.Second, func() bool {
		c.mu.Lock()
		n := len(c.inFlight)
		c.mu.Unlock()
		return n == 1
	})

	c.CloseAll()

	c.mu.Lock()
	nAfter := len(c.inFlight)
	c.mu.Unlock()
	if nAfter != 0 {
		t.Fatalf("CloseAll did not clear the in-flight registry, %d left", nAfter)
	}

	close(proceed)
	<-res

	if dialRan.Load() && entryErr == nil {
		t.Fatal("dialFunc ran with a live context after CloseAll")
	}
	c.mu.Lock()
	gate := c.gates[tag]
	c.mu.Unlock()
	if gate != nil && gate.consecutive != 0 {
		t.Fatalf("post-CloseAll dial polluted failure history: consecutive=%d", gate.consecutive)
	}
	if lines := lg.snapshot(); len(lines) != 0 {
		t.Fatalf("post-CloseAll dial produced logs: %v", lines)
	}
	c.Close()
}

// Final close must leave the epoch canceled (no live replacement).
func TestBackoffFinalCloseLeavesEpochCanceled(t *testing.T) {
	c, _ := newTestBackoff()
	c.BeginClose()
	c.CloseAll() // closing → must NOT mint a live replacement epoch
	c.mu.Lock()
	epoch := c.epoch
	c.mu.Unlock()
	if epoch.ctx.Err() == nil {
		t.Fatal("final-close epoch must remain canceled")
	}
	c.Close()
}

var _ N.Dialer = plainDialer{}
