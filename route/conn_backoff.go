package route

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
)

// connection_failure_backoff (VilaNet local patch #3)
//
// Bounds the CPU/dial cost of a dead selected outbound. sing-box has no
// backpressure on the new-connection path: with the Windows TUN system stack
// completing handshakes in-kernel, a fast-failing outbound turns app retry
// loops into an unthrottled in-process hot loop. This gate paces the dial rate
// to a failing outbound and caps in-flight dials per outer-dialer tag, from the
// first attempt.
//
// The gate is keyed by the OUTER dialer's tag (adapter.Outbound.Tag()); direct
// outbounds and non-Outbound dialers bypass it entirely. See the design plan
// docs/development/2026-07-16-windows-dead-node-cpu-spin-singbox-dial-backoff-fix-plan.md.

const (
	backoffMaxPending  = 64 // in-flight dial cap per tag (park lane depth)
	backoffTripAt      = 5  // consecutive failures required to trip
	backoffMaxShift    = 3  // clamps the pacing-delay left shift
	backoffResetGap    = 10 * time.Second
	backoffBaseDelay   = 250 * time.Millisecond
	backoffMaxDelay    = 2 * time.Second
	backoffParkTimeout = 2 * time.Second
)

var (
	errBackoffClosing  = E.New("connection_failure_backoff: manager closing")
	errBackoffOverflow = E.New("connection_failure_backoff: pending dial limit reached")
	errBackoffReset    = E.New("connection_failure_backoff: network reset")
)

// backoffTag reports the gate key for a dialer, and whether the dialer is
// gated at all. Only proxy/group outbounds are gated: direct-type outbounds
// eagerly dial the destination (a background client retrying a dead LAN host
// would keep the single global direct gate tripped), and non-Outbound dialers
// (DNS transports, urltest probes) never carry a stable failure identity.
func backoffTag(this N.Dialer) (string, bool) {
	outbound, isOutbound := this.(adapter.Outbound)
	if !isOutbound || outbound.Type() == C.TypeDirect {
		return "", false
	}
	return outbound.Tag(), true
}

// backoffEpoch is a cancellation generation. Rotating (CloseAll) or canceling
// (BeginClose) the epoch aborts every in-flight dial, parked slot waiter, and
// pacing sleeper registered against it.
type backoffEpoch struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func newBackoffEpoch() *backoffEpoch {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &backoffEpoch{ctx: ctx, cancel: cancel}
}

// permitWaiter is one entry in a gate's FIFO pacing queue. Only the head waiter
// sleeps at a time; promote is closed when this waiter becomes the head.
type permitWaiter struct {
	promote chan struct{}
}

// leafGate holds the per-tag gate state. All fields except slots are guarded by
// backoffController.mu; slots is a self-synchronizing token channel.
type leafGate struct {
	slots chan struct{} // pre-filled capacity backoffMaxPending; a token is held for the dial duration

	consecutive       int
	lastFailAt        time.Time
	successGeneration uint64 // stamps each attempt; a failure only counts if no success happened since it began
	tripped           bool
	pacedSinceTrip    int
	overflowSinceTrip int

	permitWaiters []*permitWaiter
	permitFlush   chan struct{} // closed on recovery so queued/sleeping waiters proceed immediately
}

// newLeafGate builds a gate with a caller-specified in-flight cap (production
// uses backoffMaxPending; tests shrink it for determinism).
func newLeafGate(maxPending int) *leafGate {
	g := &leafGate{slots: make(chan struct{}, maxPending)}
	for i := 0; i < maxPending; i++ {
		g.slots <- struct{}{}
	}
	return g
}

// backoffController owns every gate and the shared close/epoch state.
type backoffController struct {
	mu      sync.Mutex
	gates   map[string]*leafGate
	epoch   *backoffEpoch
	closing bool
	done    chan struct{}
	workers sync.WaitGroup
	logger  logger.ContextLogger

	// inFlight is the synchronous cancel registry: every admitted attempt's
	// dial-context cancel func, keyed by a monotonic id, registered/removed
	// under mu. It lets BeginClose/CloseAll cancel in-flight dials as a real
	// barrier (see cancelAllInFlightLocked) instead of scheduling an async hook.
	inFlight      map[uint64]context.CancelCauseFunc
	nextAttemptID uint64

	now        func() time.Time // injectable for tests
	beforeDial func()           // test-only seam: invoked after enrollment, before dialFunc
	// tunables; defaults in newBackoffController, overridable in tests.
	maxPending  int
	tripAt      int
	resetGap    time.Duration
	baseDelay   time.Duration
	maxDelay    time.Duration
	parkTimeout time.Duration
}

func newBackoffController(logger logger.ContextLogger) *backoffController {
	return &backoffController{
		gates:       make(map[string]*leafGate),
		epoch:       newBackoffEpoch(),
		done:        make(chan struct{}),
		logger:      logger,
		inFlight:    make(map[uint64]context.CancelCauseFunc),
		now:         time.Now,
		maxPending:  backoffMaxPending,
		tripAt:      backoffTripAt,
		resetGap:    backoffResetGap,
		baseDelay:   backoffBaseDelay,
		maxDelay:    backoffMaxDelay,
		parkTimeout: backoffParkTimeout,
	}
}

// pacingDelay returns the FIFO pacing delay for the current consecutive-failure
// count: min(baseDelay << min(consecutive-tripAt, maxShift), maxDelay).
func (c *backoffController) pacingDelay(consecutive int) time.Duration {
	shift := consecutive - c.tripAt
	if shift < 0 {
		shift = 0
	}
	if shift > backoffMaxShift {
		shift = backoffMaxShift
	}
	d := c.baseDelay << uint(shift)
	if d > c.maxDelay {
		d = c.maxDelay
	}
	return d
}

// runDial performs a gated TCP dial. dialFunc receives the (epoch-cancellable)
// dial context and must run exactly one of NewConnection's two dial branches.
//
// Return semantics:
//   - proceed=true: dialFunc ran to completion; (conn, err) is its natural
//     result and the failure/success history has been updated. The caller
//     continues NewConnection normally, including its error log on err != nil.
//   - proceed=false: the attempt was aborted (overflow park timeout, epoch
//     cancellation, ctx cancellation, or a stale-epoch result). Any remote conn
//     produced by dialFunc has already been closed; the caller must close the
//     inbound quietly (CloseOnHandshakeFailure) WITHOUT a terminal error log.
func (c *backoffController) runDial(ctx context.Context, tag string, dialFunc func(context.Context) (net.Conn, error)) (net.Conn, error, bool) {
	// --- Atomic admission transaction (single lock) ---
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return nil, errBackoffClosing, false
	}
	epoch := c.epoch
	gate := c.gates[tag]
	if gate == nil {
		gate = newLeafGate(c.maxPending)
		c.gates[tag] = gate
	}
	c.workers.Add(1) // registered before any closing state can be observed
	c.mu.Unlock()
	defer c.workers.Done()

	// --- Slot acquisition (in-flight cap / park lane), held for the dial duration ---
	if cause := c.acquireSlot(ctx, epoch, gate); cause != nil {
		// Only a genuine park TIMEOUT is overflow; caller/epoch/manager
		// cancellations carry their own cause and are not saturation.
		if cause == errBackoffOverflow {
			c.recordOverflow(gate)
		}
		return nil, cause, false
	}
	// Slot release is UNCONDITIONAL on the captured gate (the token count is
	// tied to the physical channel, not to epoch validity); only history
	// mutation below is epoch-conditional.
	defer func() { gate.slots <- struct{}{} }()

	// Re-check cancellation after WINNING a token: a select may hand us a slot
	// even when the caller/epoch is already dead. Never dial a doomed attempt.
	if cause := c.cancellationCause(ctx, epoch); cause != nil {
		return nil, cause, false
	}

	// --- Pacing (FIFO permit) if tripped ---
	if cause := c.waitPermit(ctx, epoch, gate); cause != nil {
		return nil, cause, false
	}

	// --- Pre-dial transition: recheck cancellation, stamp the generation, and
	// enroll the dial context in the synchronous cancel registry ALL under the
	// same lock BeginClose/CloseAll use. This closes the window where a
	// concurrent barrier could fire between the stamp and enrollment and let a
	// stale dial start uncancelable. ---
	c.mu.Lock()
	if c.closing || epoch != c.epoch || epoch.ctx.Err() != nil || ctx.Err() != nil {
		c.mu.Unlock()
		cause := c.cancellationCause(ctx, epoch)
		if cause == nil {
			cause = errBackoffReset
		}
		return nil, cause, false
	}
	attemptGen := gate.successGeneration
	dialCtx, cancel := context.WithCancelCause(ctx)
	attemptID := c.nextAttemptID
	c.nextAttemptID++
	c.inFlight[attemptID] = cancel
	c.mu.Unlock()
	// Deferred so a dialFunc panic still unregisters and releases the context.
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, attemptID)
		c.mu.Unlock()
		cancel(nil)
	}()

	if c.beforeDial != nil {
		c.beforeDial()
	}
	remote, err := dialFunc(dialCtx)

	// --- Result handling; history mutation is epoch-conditional ---
	c.mu.Lock()
	if epoch != c.epoch || epoch.ctx.Err() != nil || ctx.Err() != nil {
		// Stale epoch (rotated by CloseAll), canceled epoch (BeginClose), or a
		// caller cancellation: discard the result with no history update and no
		// terminal error log, and never retain a stale-leaf connection.
		c.mu.Unlock()
		if remote != nil {
			common.Close(remote)
		}
		cause := c.cancellationCause(ctx, epoch)
		if cause == nil {
			cause = errBackoffReset
		}
		return nil, cause, false
	}
	var pending []string
	if err != nil {
		pending = c.recordFailureLocked(gate, tag, attemptGen)
	} else {
		pending = c.recordSuccessLocked(gate, tag)
	}
	c.mu.Unlock()
	// Emit trip/recovery logs OUTSIDE the lock so a slow logger cannot block
	// admissions/CloseAll/BeginClose across every tag.
	for _, line := range pending {
		c.logger.WarnContext(ctx, line)
	}
	return remote, err, true
}

// cancellationCause reports why a gated attempt must abort without dialing, or
// nil if none applies. Caller cancellation takes priority, then manager
// shutdown, then epoch rotation/cancellation.
func (c *backoffController) cancellationCause(ctx context.Context, epoch *backoffEpoch) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	select {
	case <-c.done:
		return errBackoffClosing
	default:
	}
	if epoch.ctx.Err() != nil {
		return errBackoffReset
	}
	return nil
}

// acquireSlot takes a token from the gate's in-flight cap, parking (as an idle
// goroutine, already spawned by sing-tun) until a slot frees, the epoch/manager
// dies, the inbound ctx cancels, or the park timeout elapses. Returns nil on
// success, else the distinct abort cause (only errBackoffOverflow is a genuine
// saturation timeout).
func (c *backoffController) acquireSlot(ctx context.Context, epoch *backoffEpoch, gate *leafGate) error {
	// Honor an already-dead caller/epoch/manager before consuming a token.
	if cause := c.cancellationCause(ctx, epoch); cause != nil {
		return cause
	}
	select {
	case <-gate.slots:
		return nil
	default:
	}
	timer := time.NewTimer(c.parkTimeout)
	defer timer.Stop()
	select {
	case <-gate.slots:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-epoch.ctx.Done():
		return errBackoffReset
	case <-c.done:
		return errBackoffClosing
	case <-timer.C:
		return errBackoffOverflow
	}
}

// waitPermit enforces FIFO pacing once the gate is tripped. Returns nil if the
// caller may proceed to dial (paced grant or recovery flush), else the distinct
// abort cause.
func (c *backoffController) waitPermit(ctx context.Context, epoch *backoffEpoch, gate *leafGate) error {
	c.mu.Lock()
	if gate.consecutive < c.tripAt {
		c.mu.Unlock()
		return nil
	}
	w := &permitWaiter{promote: make(chan struct{})}
	flush := gate.permitFlush
	gate.permitWaiters = append(gate.permitWaiters, w)
	if len(gate.permitWaiters) == 1 {
		close(w.promote) // first in line becomes head immediately
	}
	c.mu.Unlock()

	// Phase 1: wait to become the head of the FIFO queue.
	select {
	case <-w.promote:
	case <-flush:
		c.leavePermit(gate, w, false)
		return nil // recovered while queued → proceed without pacing
	case <-ctx.Done():
		c.leavePermit(gate, w, false)
		return context.Cause(ctx)
	case <-epoch.ctx.Done():
		c.leavePermit(gate, w, false)
		return errBackoffReset
	case <-c.done:
		c.leavePermit(gate, w, false)
		return errBackoffClosing
	}

	// Phase 2: head of queue — sleep a freshly computed delay, then hand the
	// baton to the next waiter. A canceled head hands off WITHOUT leaving
	// schedule debt (the next head computes its own fresh delay).
	c.mu.Lock()
	delay := c.pacingDelay(gate.consecutive)
	c.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		c.leavePermit(gate, w, true)
		return nil
	case <-flush:
		c.leavePermit(gate, w, false)
		return nil // recovered mid-sleep → proceed
	case <-ctx.Done():
		c.leavePermit(gate, w, false)
		return context.Cause(ctx)
	case <-epoch.ctx.Done():
		c.leavePermit(gate, w, false)
		return errBackoffReset
	case <-c.done:
		c.leavePermit(gate, w, false)
		return errBackoffClosing
	}
}

// leavePermit removes a waiter from the FIFO queue and, if it was the head,
// promotes the next waiter. paced marks a completed pacing grant (for the
// recovery log's paced count).
func (c *backoffController) leavePermit(gate *leafGate, w *permitWaiter, paced bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := -1
	for i, waiter := range gate.permitWaiters {
		if waiter == w {
			idx = i
			break
		}
	}
	if idx >= 0 {
		gate.permitWaiters = append(gate.permitWaiters[:idx], gate.permitWaiters[idx+1:]...)
		if idx == 0 && len(gate.permitWaiters) > 0 {
			close(gate.permitWaiters[0].promote)
		}
	}
	if paced {
		gate.pacedSinceTrip++
	}
}

func (c *backoffController) recordOverflow(gate *leafGate) {
	c.mu.Lock()
	gate.overflowSinceTrip++
	c.mu.Unlock()
}

// recordFailureLocked applies the trip rule: >=tripAt consecutive failures with
// every inter-failure gap < resetGap. Late failures from a member a healthy
// dial already reset are suppressed via the success generation. Returns any
// trip/recovery log lines to emit AFTER the lock is released.
func (c *backoffController) recordFailureLocked(gate *leafGate, tag string, attemptGen uint64) []string {
	if attemptGen != gate.successGeneration {
		return nil // a success landed since this attempt began — do not re-poison
	}
	var pending []string
	now := c.now()
	if !gate.lastFailAt.IsZero() && now.Sub(gate.lastFailAt) >= c.resetGap {
		gate.consecutive = 0
		if line := c.recoverLocked(gate, tag); line != "" {
			pending = append(pending, line) // a large gap heals a tripped gate
		}
	}
	gate.consecutive++
	gate.lastFailAt = now
	if gate.consecutive == c.tripAt && !gate.tripped {
		gate.tripped = true
		gate.permitFlush = make(chan struct{})
		gate.pacedSinceTrip = 0
		gate.overflowSinceTrip = 0
		pending = append(pending, fmt.Sprintf("connection_failure_backoff: tripped tag=%s consecutive=%d", tag, gate.consecutive))
	}
	return pending
}

// recordSuccessLocked resets the run and clears any trip. Returns any recovery
// log line to emit after the lock is released.
func (c *backoffController) recordSuccessLocked(gate *leafGate, tag string) []string {
	gate.successGeneration++
	gate.consecutive = 0
	gate.lastFailAt = time.Time{}
	if line := c.recoverLocked(gate, tag); line != "" {
		return []string{line}
	}
	return nil
}

// recoverLocked clears a tripped gate: flushes the pacing queue so waiters
// proceed and returns exactly one recovery log line (transition-bounded), or ""
// if the gate was not tripped.
func (c *backoffController) recoverLocked(gate *leafGate, tag string) string {
	if !gate.tripped {
		return ""
	}
	gate.tripped = false
	if gate.permitFlush != nil {
		close(gate.permitFlush)
		gate.permitFlush = nil
	}
	gate.permitWaiters = nil
	return fmt.Sprintf("connection_failure_backoff: recovered tag=%s paced=%d overflow=%d", tag, gate.pacedSinceTrip, gate.overflowSinceTrip)
}

// cancelAllInFlightLocked synchronously cancels every registered in-flight dial
// context. context cancel funcs are cheap and non-blocking, so this is safe to
// run under c.mu.
//
// Postcondition: because enrollment happens under c.mu (the pre-dial
// transition) and this runs under the same lock, when BeginClose/CloseAll
// returns EVERY already-admitted attempt's dialCtx is already canceled. A
// dialFunc invoked after the barrier therefore runs with a canceled context and
// aborts before network I/O. (Preventing the invocation itself is impossible
// without holding the lock across the dial; canceled-at-entry is the correct
// achievable invariant.)
func (c *backoffController) cancelAllInFlightLocked(cause error) {
	for id, cancel := range c.inFlight {
		cancel(cause)
		delete(c.inFlight, id)
	}
}

// CloseAll cancels the current epoch and every in-flight dial (aborting parkers,
// sleepers and active dials) and drops every gate, under a single lock. A live
// replacement epoch is minted only while the controller is still usable —
// during final close we leave the canceled epoch in place so the final-close
// postcondition (no live epoch) holds.
func (c *backoffController) CloseAll() {
	c.mu.Lock()
	c.epoch.cancel(errBackoffReset)
	c.cancelAllInFlightLocked(errBackoffReset)
	if !c.closing {
		c.epoch = newBackoffEpoch()
	}
	c.gates = make(map[string]*leafGate)
	c.mu.Unlock()
}

// BeginClose is idempotent: it cancels the current epoch, synchronously cancels
// every in-flight dial, and rejects new admissions. Invoked at the top of
// Box.Close() before outbound shutdown so no gated dial can start against a
// half-closed outbound.
func (c *backoffController) BeginClose() {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.epoch.cancel(errBackoffReset)
		c.cancelAllInFlightLocked(errBackoffClosing)
	}
	c.mu.Unlock()
}

// Close completes shutdown: reject admissions, wake any remaining waiters, and
// wait for every registered worker to drain.
func (c *backoffController) Close() {
	c.BeginClose()
	close(c.done)
	c.workers.Wait()
}
