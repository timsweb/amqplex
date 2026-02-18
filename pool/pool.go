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
	Connection      Connection
	ChannelMappings map[int]int // client channel -> upstream channel
	mu              sync.RWMutex
}

type ConnectionPool struct {
	Username    string
	Password    string
	Vhost       string
	IdleTimeout time.Duration
	MaxChannels int
	Connections []*PooledConnection
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
	}
}

func (p *ConnectionPool) AddConnection(conn Connection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pooled := &PooledConnection{
		Connection:      conn,
		ChannelMappings: make(map[int]int),
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
