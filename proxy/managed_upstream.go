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

	mu               sync.Mutex
	conn             *UpstreamConn
	usedChannels     map[uint16]bool
	channelOwners    map[uint16]channelEntry
	pendingClose     map[uint16]bool // channels awaiting CloseOk; slot held until CloseOk arrives
	nextHint         uint16          // next channel ID to try; protected by mu
	clients          []clientWriter
	lastEmptyTime    time.Time  // protected by mu; zero when clients > 0
	upstreamWriterMu sync.Mutex // serialises all writes to conn.Writer

	stopped        atomic.Bool
	reconnectTotal atomic.Int64 // cumulative reconnect attempts since start
	heartbeat      uint16       // negotiated heartbeat interval in seconds

	upstreamAddr string // "host:port" — used in log fields
	logger       *slog.Logger
}

// AllocateChannel finds the lowest free upstream channel ID, registers the
// mapping, and returns the upstream ID. Returns an error if maxChannels is
// exhausted.
func (m *ManagedUpstream) AllocateChannel(clientChanID uint16, cw clientWriter) (uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Fast path: scan forward from hint
	for id := m.nextHint; id <= m.maxChannels; id++ {
		if !m.usedChannels[id] && !m.pendingClose[id] {
			m.usedChannels[id] = true
			m.channelOwners[id] = channelEntry{owner: cw, clientChanID: clientChanID}
			m.nextHint = id + 1
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
}

// HasCapacity reports whether this upstream has at least one free channel slot.
func (m *ManagedUpstream) HasCapacity() bool {
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

// writeFrameToUpstream serialises writes to the upstream connection.
func (m *ManagedUpstream) writeFrameToUpstream(frame *Frame) error {
	m.upstreamWriterMu.Lock()
	defer m.upstreamWriterMu.Unlock()
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return nil
	}
	if err := WriteFrame(conn.Writer, frame); err != nil {
		return err
	}
	return conn.Writer.Flush()
}

// Start sets the upstream connection and launches the read loop goroutine.
// Must be called exactly once after creation.
func (m *ManagedUpstream) Start(conn *UpstreamConn) {
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
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
			_ = m.writeFrameToUpstream(hb)

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
					continue
				}
				// Client-initiated close: deliver CloseOk to the client, then clean up.
				entry, ok := m.channelOwners[frame.Channel]
				if ok {
					delete(m.channelOwners, frame.Channel)
					delete(m.usedChannels, frame.Channel)
				}
				m.mu.Unlock()
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

	m.mu.Lock()
	clients := append([]clientWriter(nil), m.clients...)
	m.mu.Unlock()

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
	if m.conn != nil {
		m.conn.Conn.Close()
		m.conn = nil
	}
	return true
}

// reconnectLoop attempts to re-establish the upstream connection with
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
