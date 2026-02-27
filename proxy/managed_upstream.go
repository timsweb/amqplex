package proxy

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// clientWriter is the interface ManagedUpstream uses to interact with a
// registered client connection. ClientConnection implements this.
type clientWriter interface {
	DeliverFrame(frame *Frame) error
	Abort()
	// OnChannelClosed is called by readLoop after delivering Channel.CloseOk for a
	// client-initiated close. It removes the client-side channel mapping so the slot
	// is not double-closed by releaseClientChannels on disconnect.
	OnChannelClosed(clientChanID uint16)
}

// channelEntry binds an upstream channel ID to the client that owns it and
// the client-side channel ID used for remapping.
type channelEntry struct {
	owner        clientWriter
	clientChanID uint16
}

// ManagedUpstream owns one upstream AMQP connection shared by multiple clients.
// One instance exists per (username, password, vhost) credential set.
type ManagedUpstream struct {
	username, password, vhost string
	maxChannels               uint16

	// dialFn dials a new UpstreamConn. Injected for testability; set by Proxy in production.
	dialFn        func() (*UpstreamConn, error)
	reconnectBase time.Duration // base backoff for reconnect; defaults to 500ms

	mu            sync.Mutex
	conn          *UpstreamConn
	usedChannels  map[uint16]bool
	channelOwners map[uint16]channelEntry
	pendingClose  map[uint16]bool // channels awaiting CloseOk; slot held until CloseOk arrives
	nextHint      uint16          // next channel ID to try; protected by mu
	clients       []clientWriter
	lastEmptyTime time.Time // protected by mu; zero when clients > 0

	writeCh     chan *Frame    // write pump input; buffered
	writeDoneMu sync.Mutex     // protects writeDone replacement across reconnects
	writeDone   chan struct{}  // current write pump stop channel; replaced each reconnect
	pumpWg      sync.WaitGroup // tracks the single live writePump goroutine

	stopped        atomic.Bool
	reconnecting   atomic.Bool  // true while reconnectLoop is running; HasCapacity returns false
	reconnectTotal atomic.Int64 // cumulative reconnect attempts since start
	heartbeat      uint16       // negotiated heartbeat interval in seconds

	upstreamAddr string // "host:port" — used in log fields
	logger       *slog.Logger
}

// AllocateChannel finds the lowest free upstream channel ID, registers the
// mapping, and returns the upstream ID. Returns an error if maxChannels is
// exhausted or if the upstream is currently reconnecting.
//
// The reconnecting check guards the narrow window between the post-Register
// check in handleConnection and this call: a reconnect could start after the
// client passes the register check but before it opens a channel. Failing fast
// here lets the client retry with a fresh upstream rather than opening a
// channel during a state-reset in progress.
func (m *ManagedUpstream) AllocateChannel(clientChanID uint16, cw clientWriter) (uint16, error) {
	if m.reconnecting.Load() {
		if m.logger != nil {
			m.logger.Debug("channel allocation rejected — upstream reconnecting",
				slog.Int("client_chan", int(clientChanID)),
				slog.String("user", m.username),
				slog.String("vhost", m.vhost),
			)
		}
		return 0, errors.New("upstream reconnecting")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Fast path: scan forward from hint. nextHint starts at 1 and must never be
	// 0 — channel 0 is the AMQP connection-level channel and is illegal for
	// regular use. uint16 overflows to 0 when id=65535; guard against that here.
	if m.nextHint == 0 {
		m.nextHint = 1
	}
	for id := m.nextHint; id <= m.maxChannels; id++ {
		if !m.usedChannels[id] && !m.pendingClose[id] {
			m.usedChannels[id] = true
			m.channelOwners[id] = channelEntry{owner: cw, clientChanID: clientChanID}
			m.nextHint = id + 1
			if m.nextHint == 0 { // uint16 overflow guard: 65535+1 wraps to 0
				m.nextHint = 1
			}
			// Note: logger.Debug is called while m.mu is held. This is safe with the
			// stdlib slog handlers (text/JSON/discard) but would deadlock if a custom
			// handler acquired m.mu. Keep handlers side-effect-free.
			if m.logger != nil {
				m.logger.Debug("channel allocated",
					slog.Int("client_chan", int(clientChanID)),
					slog.Int("upstream_chan", int(id)),
					slog.String("user", m.username),
					slog.String("vhost", m.vhost),
				)
			}
			return id, nil
		}
	}
	// Wrap around: scan from 1 up to hint
	for id := uint16(1); id < m.nextHint; id++ {
		if !m.usedChannels[id] && !m.pendingClose[id] {
			m.usedChannels[id] = true
			m.channelOwners[id] = channelEntry{owner: cw, clientChanID: clientChanID}
			m.nextHint = id + 1
			if m.nextHint == 0 { // uint16 overflow guard
				m.nextHint = 1
			}
			// Note: logger.Debug is called while m.mu is held. This is safe with the
			// stdlib slog handlers (text/JSON/discard) but would deadlock if a custom
			// handler acquired m.mu. Keep handlers side-effect-free.
			if m.logger != nil {
				m.logger.Debug("channel allocated",
					slog.Int("client_chan", int(clientChanID)),
					slog.Int("upstream_chan", int(id)),
					slog.String("user", m.username),
					slog.String("vhost", m.vhost),
				)
			}
			return id, nil
		}
	}
	return 0, errors.New("no free upstream channel available")
}

// ReleaseChannel marks an upstream channel ID as free.
func (m *ManagedUpstream) ReleaseChannel(upstreamChanID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.usedChannels, upstreamChanID)
	delete(m.channelOwners, upstreamChanID)
	if upstreamChanID < m.nextHint {
		m.nextHint = upstreamChanID
	}
	// Note: logger.Debug is called while m.mu is held. This is safe with the
	// stdlib slog handlers (text/JSON/discard) but would deadlock if a custom
	// handler acquired m.mu. Keep handlers side-effect-free.
	if m.logger != nil {
		m.logger.Debug("channel released",
			slog.Int("upstream_chan", int(upstreamChanID)),
			slog.String("user", m.username),
			slog.String("vhost", m.vhost),
		)
	}
}

// ScheduleChannelClose marks an upstream channel as pending close after the proxy
// sends Channel.Close on the client's behalf during cleanup. The channel slot stays
// reserved in usedChannels (so it cannot be reallocated) and channelOwners is
// cleared (so no frames are forwarded to the old client). When Channel.CloseOk
// arrives from the broker, readLoop removes the slot from both maps.
func (m *ManagedUpstream) ScheduleChannelClose(upstreamChanID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingClose[upstreamChanID] = true
	delete(m.channelOwners, upstreamChanID)
	if m.logger != nil {
		m.logger.Debug("channel scheduled for close (proxy-initiated)",
			slog.Int("upstream_chan", int(upstreamChanID)),
			slog.String("user", m.username),
			slog.String("vhost", m.vhost),
		)
	}
}

// ScheduleChannelCloseIfOwnedBy marks an upstream channel as pending close only
// if channelOwners still records it as belonging to cw. Returns true if the
// channel was scheduled (caller should send Channel.Close to the broker); false
// if the entry is absent or belongs to a different client.
//
// This is the single ownership-aware scheduling primitive used in two cases:
//
//  1. Client-initiated close in flight: client already sent Channel.Close but
//     the proxy has not yet received Channel.CloseOk. The slot must stay reserved
//     until CloseOk arrives to prevent a new Channel.Open from reaching the broker
//     while the previous close is still in flight (which causes a 504 CHANNEL_ERROR).
//
//  2. Client disconnected with channel open: the proxy needs to send Channel.Close
//     on the client's behalf, but only if the upstream connection hasn't been reset
//     (e.g. due to a prior 504). After reconnect channelOwners is wiped, so old
//     goroutines' stale cleanup calls return false and send nothing — preventing
//     the self-perpetuating 504 cycle where Channel.Close for a never-opened channel
//     triggers another 504.
//
// Checking the owner (not just usedChannels) is critical: between the moment
// readLoop frees the slot (deletes usedChannels[id] and channelOwners[id]) and
// the moment this function runs, a new client may have already re-allocated the
// same channel number. Checking usedChannels alone would incorrectly mark the
// new client's channel as pending close. Checking the owner pointer ensures we
// only act when the channel still belongs to the disconnecting client.
func (m *ManagedUpstream) ScheduleChannelCloseIfOwnedBy(upstreamChanID uint16, cw clientWriter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.channelOwners[upstreamChanID]
	if ok && entry.owner == cw {
		// Channel still registered to this client; hold the slot in pendingClose
		// until CloseOk arrives from the broker.
		m.pendingClose[upstreamChanID] = true
		delete(m.channelOwners, upstreamChanID)
		if m.logger != nil {
			m.logger.Debug("channel scheduled for close (ownership confirmed)",
				slog.Int("upstream_chan", int(upstreamChanID)),
				slog.String("user", m.username),
				slog.String("vhost", m.vhost),
			)
		}
		return true
	}
	// Entry absent (CloseOk already freed the slot, or upstream was reconnected
	// and state was reset) or belongs to a different client (reallocated).
	// In either case do nothing — the slot is either already clean or belongs
	// to someone else.
	if m.logger != nil {
		m.logger.Debug("channel close skipped — already freed, reallocated, or upstream reconnected",
			slog.Int("upstream_chan", int(upstreamChanID)),
			slog.Bool("entry_present", ok),
			slog.String("user", m.username),
			slog.String("vhost", m.vhost),
		)
	}
	return false
}

// HasCapacity reports whether this upstream has at least one free channel slot
// and is not currently reconnecting. Returns false during reconnect so that
// getOrCreateManagedUpstream routes new clients to a fresh upstream instead of
// attaching them to a connection whose channel state is being reset.
func (m *ManagedUpstream) HasCapacity() bool {
	if m.reconnecting.Load() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return uint16(len(m.usedChannels)) < m.maxChannels
}

// Register adds a client to the teardown list.
func (m *ManagedUpstream) Register(cw clientWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = append(m.clients, cw)
	m.lastEmptyTime = time.Time{} // upstream is active
}

// AbortAllClients closes all registered client connections, causing their
// handleConnection goroutines to exit promptly.
func (m *ManagedUpstream) AbortAllClients() {
	m.mu.Lock()
	clients := append([]clientWriter(nil), m.clients...)
	m.mu.Unlock()
	for _, cw := range clients {
		cw.Abort()
	}
}

// Deregister removes a client from the teardown list.
func (m *ManagedUpstream) Deregister(cw clientWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for i, c := range m.clients {
		if c == cw {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			found = true
			break
		}
	}
	if found && len(m.clients) == 0 {
		m.lastEmptyTime = time.Now()
	}
}

// writeFrameToUpstream enqueues a frame for the write pump.
// Returns immediately; the pump handles serialisation and flushing.
// No-ops if the upstream is stopped, reconnecting, or write pump
// has been signalled to stop.
func (m *ManagedUpstream) writeFrameToUpstream(frame *Frame) {
	if m.stopped.Load() || m.reconnecting.Load() {
		if m.reconnecting.Load() && m.logger != nil {
			m.logger.Debug("frame dropped — upstream reconnecting",
				slog.Int("channel", int(frame.Channel)),
				slog.Int("frame_type", int(frame.Type)),
				slog.String("user", m.username),
				slog.String("vhost", m.vhost),
			)
		}
		return
	}
	m.writeDoneMu.Lock()
	done := m.writeDone
	m.writeDoneMu.Unlock()
	select {
	case m.writeCh <- frame:
	case <-done:
	}
}

// writePump is the sole writer to conn.Writer. It reads frames from writeCh,
// batches any frames that are already queued, then flushes once per batch.
// It exits on write error (calling handleUpstreamFailure) or when done is
// closed (clean shutdown / idle expiry / replaced by reconnect).
// done is captured at start time so that reconnectLoop can kill this specific
// pump instance by closing its done channel without affecting the replacement.
func (m *ManagedUpstream) writePump(conn *UpstreamConn, done <-chan struct{}) {
	defer m.pumpWg.Done()
	for {
		var frame *Frame
		select {
		case frame = <-m.writeCh:
		case <-done:
			return
		}

		if err := WriteFrame(conn.Writer, frame); err != nil {
			if !m.stopped.Load() {
				m.handleUpstreamFailure(err)
			}
			return
		}

		// Drain any frames already queued (batch flush).
		draining := true
		for draining {
			select {
			case next := <-m.writeCh:
				if err := WriteFrame(conn.Writer, next); err != nil {
					if !m.stopped.Load() {
						m.handleUpstreamFailure(err)
					}
					return
				}
			default:
				draining = false
			}
		}

		if err := conn.Writer.Flush(); err != nil {
			if !m.stopped.Load() {
				m.handleUpstreamFailure(err)
			}
			return
		}
	}
}

func (m *ManagedUpstream) stopWritePump() {
	m.writeDoneMu.Lock()
	defer m.writeDoneMu.Unlock()
	select {
	case <-m.writeDone:
		// already closed
	default:
		close(m.writeDone)
	}
}

// Start sets the upstream connection and launches the read loop goroutine.
// Must be called exactly once after creation.
func (m *ManagedUpstream) Start(conn *UpstreamConn) {
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
	m.writeDoneMu.Lock()
	done := m.writeDone
	m.writeDoneMu.Unlock()
	m.pumpWg.Add(1)
	go m.writePump(conn, done) // pass conn directly; pump owns it for writing
	go m.readLoop()
	if m.logger != nil {
		m.logger.Info("upstream connected",
			slog.String("upstream_addr", m.upstreamAddr),
			slog.String("user", m.username),
			slog.String("vhost", m.vhost),
		)
	}
}

func (m *ManagedUpstream) readLoop() {
	for {
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		if conn == nil {
			return
		}

		frame, err := ParseFrame(conn.Reader)
		if err != nil {
			if !m.stopped.Load() {
				m.handleUpstreamFailure(err)
			}
			return
		}

		switch {
		case frame.Type == FrameTypeHeartbeat:
			// Echo heartbeat back to upstream; do not forward to clients.
			hb := &Frame{Type: FrameTypeHeartbeat, Channel: 0, Payload: []byte{}}
			m.writeFrameToUpstream(hb)

		case frame.Channel == 0:
			// Connection-level frame (e.g. Connection.Close from upstream).
			// Forward to all registered clients and abort them.
			m.mu.Lock()
			clients := append([]clientWriter(nil), m.clients...)
			m.mu.Unlock()
			for _, cw := range clients {
				_ = cw.DeliverFrame(frame)
				cw.Abort()
			}

		default:
			if isChannelCloseOk(frame) {
				m.mu.Lock()
				if m.pendingClose[frame.Channel] {
					// Proxy-initiated close: discard, just release slot.
					delete(m.pendingClose, frame.Channel)
					delete(m.usedChannels, frame.Channel)
					m.mu.Unlock()
					if m.logger != nil {
						m.logger.Debug("Channel.CloseOk received — proxy-initiated, slot freed",
							slog.Int("upstream_chan", int(frame.Channel)),
							slog.String("user", m.username),
							slog.String("vhost", m.vhost),
						)
					}
					continue
				}
				// Client-initiated close: deliver CloseOk to the client, then clean up.
				entry, ok := m.channelOwners[frame.Channel]
				if ok {
					delete(m.channelOwners, frame.Channel)
					delete(m.usedChannels, frame.Channel)
				}
				m.mu.Unlock()
				if m.logger != nil {
					m.logger.Debug("Channel.CloseOk received — client-initiated",
						slog.Int("upstream_chan", int(frame.Channel)),
						slog.Bool("owner_found", ok),
						slog.String("user", m.username),
						slog.String("vhost", m.vhost),
					)
				}
				if ok {
					remapped := *frame
					remapped.Channel = entry.clientChanID
					_ = entry.owner.DeliverFrame(&remapped)
					entry.owner.OnChannelClosed(entry.clientChanID)
				}
				continue
			}
			// Remap channel and dispatch to the owning client.
			m.mu.Lock()
			entry, ok := m.channelOwners[frame.Channel]
			m.mu.Unlock()
			if !ok {
				continue
			}
			remapped := *frame
			remapped.Channel = entry.clientChanID
			_ = entry.owner.DeliverFrame(&remapped)
		}
	}
}

// handleUpstreamFailure tears down all clients and schedules reconnection.
func (m *ManagedUpstream) handleUpstreamFailure(cause error) {
	if m.logger != nil {
		errStr := ""
		if cause != nil {
			errStr = cause.Error()
		}
		m.logger.Warn("upstream lost",
			slog.String("upstream_addr", m.upstreamAddr),
			slog.String("user", m.username),
			slog.String("vhost", m.vhost),
			slog.String("error", errStr),
		)
	}

	// Only the first caller (writePump or readLoop) starts a reconnect. When the
	// upstream TCP connection dies both goroutines get errors nearly simultaneously
	// and both call handleUpstreamFailure. Without this guard, two reconnectLoops
	// would run concurrently: each dials a new RabbitMQ connection, resets shared
	// channel state, and starts its own writePump+readLoop. The two writePumps
	// then race to dequeue from the shared writeCh and send frames to different
	// connections in garbled order — Channel.Open can arrive mid-handshake on the
	// wrong connection, causing RabbitMQ to report
	// "unexpected method in connection state running".
	if !m.reconnecting.CompareAndSwap(false, true) {
		return // another goroutine is already handling reconnect
	}

	// Close the connection immediately so any writePump goroutine that is
	// mid-write on the old conn gets an error and exits promptly. Without this,
	// reconnectLoop's pumpWg.Wait() could block for the full TCP timeout.
	m.mu.Lock()
	oldConn := m.conn
	clients := append([]clientWriter(nil), m.clients...)
	m.mu.Unlock()
	if oldConn != nil {
		oldConn.Conn.Close()
	}

	for _, cw := range clients {
		cw.Abort()
	}

	go m.reconnectLoop()
}

// markStoppedIfIdle checks whether the upstream has been empty for at least
// timeout and, if so, atomically marks it stopped and closes the connection.
// Returns true if the upstream was stopped by this call.
func (m *ManagedUpstream) markStoppedIfIdle(timeout time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastEmptyTime.IsZero() {
		return false
	}
	if len(m.clients) > 0 || len(m.usedChannels) > 0 {
		return false
	}
	if time.Since(m.lastEmptyTime) < timeout {
		return false
	}
	m.stopped.Store(true)
	m.stopWritePump()
	if m.conn != nil {
		m.conn.Conn.Close()
		m.conn = nil
	}
	return true
}

// reconnectLoop attempts to re-establish the upstream connection with
// exponential backoff. It relaunches readLoop once a connection succeeds.
//
// CRITICAL: The reconnecting flag is used to prevent race conditions
// where clients attempt to allocate channels or send frames during the
// reconnection window. Three safeguards prevent these races:
//
//  1. AllocateChannel checks reconnecting and returns "upstream reconnecting"
//     if set, preventing new channel allocations during reconnect.
//
//  2. writeFrameToUpstream checks reconnecting and no-ops if set,
//     preventing frames from being sent during reconnect (which would be
//     discarded by the drain loop or sent to the wrong connection).
//
//  3. handleConnection checks HasCapacity (which checks reconnecting)
//     before registering clients, preventing new clients from being
//     attached to a reconnecting upstream.
//
// Without these safeguards, the following race can occur:
//   - Client passes HasCapacity check
//   - Client sends Channel.Open → frame enqueued
//   - Upstream fails → reconnecting = true
//   - reconnectLoop drains writeCh → Channel.Open discarded
//   - New connection established
//   - Client sends Basic.Publish → 504 CHANNEL_ERROR
//     (channel never opened on the broker)
//
// exponential backoff. It relaunches readLoop once a connection succeeds.
func (m *ManagedUpstream) reconnectLoop() {
	base := m.reconnectBase
	if base == 0 {
		base = 500 * time.Millisecond
	}
	wait := base
	const maxWait = 30 * time.Second

	attempt := 0
	for !m.stopped.Load() {
		attempt++
		m.reconnectTotal.Add(1)
		if m.logger != nil {
			m.logger.Warn("upstream reconnecting",
				slog.String("upstream_addr", m.upstreamAddr),
				slog.String("user", m.username),
				slog.String("vhost", m.vhost),
				slog.Int("attempt", attempt),
				slog.Int64("backoff_ms", wait.Milliseconds()),
			)
		}
		time.Sleep(wait)

		conn, err := m.dialFn()
		if err != nil {
			if wait*2 > maxWait {
				wait = maxWait
			} else {
				wait = wait * 2
			}
			continue
		}

		m.mu.Lock()
		m.conn = conn
		// Reset channel state — all clients were torn down on failure
		m.usedChannels = make(map[uint16]bool)
		m.channelOwners = make(map[uint16]channelEntry)
		m.pendingClose = make(map[uint16]bool)
		m.nextHint = 1
		m.clients = make([]clientWriter, 0)
		m.lastEmptyTime = time.Now() // no clients yet after reconnect
		m.mu.Unlock()

		// Kill the old writePump. If this reconnect was triggered by readLoop
		// (not writePump), the old pump may still be alive — blocked on
		// select { case frame = <-m.writeCh: ... case <-done: ... }. We must
		// stop it before starting a new pump; otherwise both goroutines compete
		// for frames from writeCh. The old pump would write to the dead conn,
		// get a write error, call handleUpstreamFailure, win the CAS (since
		// reconnecting was cleared), and start a third reconnect loop.
		//
		// Sequence:
		//   1. Swap writeDone — old pump holds a reference to oldDone; new pump
		//      gets newDone. Closing oldDone signals the old pump to exit.
		//   2. Close oldDone — old pump exits (via <-done or after write error
		//      on the conn we already closed in handleUpstreamFailure).
		//   3. pumpWg.Wait() — block until old pump's defer pumpWg.Done() fires,
		//      guaranteeing no overlap with the new pump.
		m.writeDoneMu.Lock()
		oldDone := m.writeDone
		m.writeDone = make(chan struct{})
		newDone := m.writeDone
		m.writeDoneMu.Unlock()
		close(oldDone)
		m.pumpWg.Wait() // old pump must fully exit before new pump starts

		// Drain stale frames queued before failure (all old clients were aborted,
		// and ScheduleChannelCloseIfOwnedBy returns false for wiped channelOwners,
		// so no new stale frames can arrive after the state reset above).
		for len(m.writeCh) > 0 {
			<-m.writeCh
		}

		// Clear the reconnecting flag only after draining stale frames and
		// confirming the old pump has exited. New clients that arrive after this
		// point will see HasCapacity() == true and can safely allocate channels
		// on the fresh connection.
		m.reconnecting.Store(false)

		m.pumpWg.Add(1)
		go m.writePump(conn, newDone)
		go m.readLoop()
		if m.logger != nil {
			m.logger.Info("upstream reconnected",
				slog.String("upstream_addr", m.upstreamAddr),
				slog.String("user", m.username),
				slog.String("vhost", m.vhost),
				slog.Int("attempt", attempt),
			)
		}
		return
	}
}
