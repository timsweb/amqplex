package pool

import (
	"sync"
	"time"
)

type Credentials struct {
	Username string
	Password string
	Vhost    string
}

type Connection interface {
	IsOpen() bool
	Close() error
	Channel() (Channel, error)
}

type PooledConnection struct {
	Connection   Connection
	SafeChannels map[uint16]bool
	mu           sync.RWMutex
}

type ConnectionPool struct {
	Username    string
	Password    string
	Vhost       string
	IdleTimeout time.Duration
	MaxChannels int
	Connections []*PooledConnection
	LastUsed    time.Time
	mu          sync.RWMutex
}

func NewConnectionPool(username, password, vhost string, idleTimeout int, maxChannels int) *ConnectionPool {
	return &ConnectionPool{
		Username:    username,
		Password:    password,
		Vhost:       vhost,
		IdleTimeout: time.Duration(idleTimeout) * time.Second,
		MaxChannels: maxChannels,
		Connections: make([]*PooledConnection, 0),
		LastUsed:    time.Now(),
	}
}

func (p *ConnectionPool) AddConnection(conn Connection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pooled := &PooledConnection{
		Connection:   conn,
		SafeChannels: make(map[uint16]bool),
	}
	p.Connections = append(p.Connections, pooled)
}

func (p *ConnectionPool) GetConnection() *PooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.Connections) == 0 {
		return nil
	}
	return p.Connections[0]
}

func (p *ConnectionPool) GetConnectionByIndex(i int) *PooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if i < 0 || i >= len(p.Connections) {
		return nil
	}
	return p.Connections[i]
}

func (pc *PooledConnection) AddSafeChannel(upstreamID uint16) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.SafeChannels[upstreamID] = true
}

func (pc *PooledConnection) IsSafeChannel(upstreamID uint16) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.SafeChannels[upstreamID]
}

func (pc *PooledConnection) RemoveSafeChannel(upstreamID uint16) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	delete(pc.SafeChannels, upstreamID)
}

func (p *ConnectionPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.Connections {
		if conn.Connection != nil {
			conn.Connection.Close()
		}
	}
	p.Connections = nil
	return nil
}
