package proxy

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/pool"
)

const (
	AMQPProtocolHeader = "AMQP\x00\x00\x09\x01"
)

type AMQPHandshake struct {
	Protocol string
	Username string
	Password string
	Vhost    string
}

type Proxy struct {
	listener *AMQPListener
	config   *config.Config
	pools    map[[32]byte]*pool.ConnectionPool // key: hash of credentials
	mu       sync.RWMutex
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
	listener, err := NewAMQPListener(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	return &Proxy{
		listener: listener,
		config:   cfg,
		pools:    make(map[[32]byte]*pool.ConnectionPool),
	}, nil
}

func (p *Proxy) Start() error {
	var listener net.Listener
	var err error

	if p.config.TLSCert != "" && p.config.TLSKey != "" {
		listener, err = p.listener.StartTLS()
	} else {
		listener, err = p.listener.StartPlain()
	}

	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go p.handleConnection(conn)
	}
}

func (p *Proxy) getPoolKey(username, password, vhost string) [32]byte {
	credentials := fmt.Sprintf("%s:%s:%s", username, password, vhost)
	return sha256.Sum256([]byte(credentials))
}

func (p *Proxy) getOrCreatePool(username, password, vhost string) *pool.ConnectionPool {
	key := p.getPoolKey(username, password, vhost)

	p.mu.RLock()
	existing, exists := p.pools[key]
	p.mu.RUnlock()

	if exists {
		return existing
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if existing, exists := p.pools[key]; exists {
		return existing
	}

	newPool := pool.NewConnectionPool(username, password, vhost, p.config.PoolIdleTimeout, p.config.PoolMaxChannels)
	p.pools[key] = newPool
	return newPool
}

func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	// Parse AMQP handshake and extract credentials
	username, password, vhost, err := p.parseAMQPHandshake(clientConn)
	if err != nil {
		return
	}

	// Get or create pool for these credentials
	connPool := p.getOrCreatePool(username, password, vhost)

	// TODO: Establish upstream connection and proxy traffic
	_ = connPool
}

func (p *Proxy) parseAMQPHandshake(conn net.Conn) (string, string, string, error) {
	// Read protocol header (ensure all 8 bytes are read)
	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", "", "", err
	}

	if string(header) != AMQPProtocolHeader {
		return "", "", "", fmt.Errorf("invalid AMQP protocol header")
	}

	// TODO: Parse full AMQP handshake to extract credentials
	// For now, return default credentials
	return "guest", "guest", "/", nil
}

func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pool := range p.pools {
		pool.Close()
	}

	p.pools = make(map[[32]byte]*pool.ConnectionPool)
	return nil
}
