package proxy

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"sync"

	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/pool"
	"github.com/tim/amqproxy/tlsutil"
)

type Proxy struct {
	listenAddr string
	config     *config.Config
	pools      map[[32]byte]*pool.ConnectionPool // key: hash of credentials
	tlsConfig  *tls.Config
	mu         sync.RWMutex
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
	tlsConfig, err := tlsutil.LoadTLSConfig(
		cfg.TLSCACert,
		cfg.TLSClientCert,
		cfg.TLSClientKey,
		cfg.TLSSkipVerify,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS config: %w", err)
	}

	return &Proxy{
		listenAddr: fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort),
		config:     cfg,
		pools:      make(map[[32]byte]*pool.ConnectionPool),
		tlsConfig:  tlsConfig,
	}, nil
}

func (p *Proxy) Start() error {
	listener, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
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
	// TODO: Parse full AMQP handshake to extract credentials
	// For now, return default credentials
	return "guest", "guest", "/", nil
}
