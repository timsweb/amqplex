package proxy

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// clientWriter is the interface ManagedUpstream uses to interact with a
// registered client connection. ClientConnection implements this.
type clientWriter interface {
	DeliverFrame(frame *Frame) error
	Abort()
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
	clients       []clientWriter

	stopped   atomic.Bool
	heartbeat uint16 // negotiated heartbeat interval in seconds
}

// AllocateChannel finds the lowest free upstream channel ID, registers the
// mapping, and returns the upstream ID. Returns an error if maxChannels is
// exhausted.
func (m *ManagedUpstream) AllocateChannel(clientChanID uint16, cw clientWriter) (uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := uint16(1); id <= m.maxChannels; id++ {
		if !m.usedChannels[id] {
			m.usedChannels[id] = true
			m.channelOwners[id] = channelEntry{owner: cw, clientChanID: clientChanID}
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
}

// Deregister removes a client from the teardown list.
func (m *ManagedUpstream) Deregister(cw clientWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.clients {
		if c == cw {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			return
		}
	}
}
