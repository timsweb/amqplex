package proxy

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/timsweb/amqproxy/config"
	"github.com/timsweb/amqproxy/pool"
	"github.com/timsweb/amqproxy/tlsutil"
)

type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	pools       map[[32]byte]*pool.ConnectionPool // key: hash of credentials
	netListener net.Listener
	mu          sync.RWMutex
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

	p.mu.Lock()
	p.netListener = listener
	p.mu.Unlock()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if isClosedError(err) {
				return nil
			}
			// Transient error: continue accepting
			continue
		}
		go p.handleConnection(conn)
	}
}

func isClosedError(err error) bool {
	return errors.Is(err, net.ErrClosed)
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

func parseUpstreamURL(rawURL string) (network, addr string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid upstream URL: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	switch u.Scheme {
	case "amqp":
		if port == "" {
			port = "5672"
		}
		return "tcp", net.JoinHostPort(host, port), nil
	case "amqps":
		if port == "" {
			port = "5671"
		}
		return "tcp+tls", net.JoinHostPort(host, port), nil
	default:
		return "", "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	cc := NewClientConnection(clientConn, p)

	// Step 1: Read the AMQP protocol header from the client.
	header := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, header); err != nil {
		return
	}
	if string(header) != ProtocolHeader {
		return
	}

	// Step 2: Perform the client-side AMQP handshake. This sends Connection.Start,
	// reads credentials, sends Tune, reads TuneOk, reads Connection.Open (vhost).
	if err := cc.Handle(); err != nil {
		return
	}

	// cc.Pool is now set with the correct credentials and vhost.
	cc.Mu.RLock()
	connPool := cc.Pool
	cc.Mu.RUnlock()
	if connPool == nil {
		return
	}

	// Step 3: Establish upstream connection.
	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
	if err != nil {
		return
	}

	var upstreamNetConn net.Conn
	if network == "tcp+tls" {
		tlsCfg, err := p.upstreamTLSConfig()
		if err != nil {
			return
		}
		upstreamNetConn, err = tls.Dial("tcp", addr, tlsCfg)
	} else {
		upstreamNetConn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return
	}
	defer upstreamNetConn.Close()

	upstreamConn, err := performUpstreamHandshake(upstreamNetConn, connPool.Username, connPool.Password, connPool.Vhost)
	if err != nil {
		return
	}

	// Step 4: Bidirectional frame proxy.
	fp := NewFrameProxy(cc, upstreamConn.Writer, cc.Writer)
	done := make(chan struct{}, 2)

	// Client → Upstream
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			frame, err := ParseFrame(cc.Reader)
			if err != nil {
				return
			}
			if err := fp.ProxyClientToUpstream(frame); err != nil {
				return
			}
			if err := upstreamConn.Writer.Flush(); err != nil {
				return
			}
		}
	}()

	// Upstream → Client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			frame, err := ParseFrame(upstreamConn.Reader)
			if err != nil {
				return
			}
			if err := fp.ProxyUpstreamToClient(frame); err != nil {
				return
			}
			if err := cc.Writer.Flush(); err != nil {
				return
			}
		}
	}()

	// Wait for either direction to finish, then close both connections.
	<-done
}

func (p *Proxy) upstreamTLSConfig() (*tls.Config, error) {
	return tlsutil.LoadTLSConfig(
		p.config.TLSCACert,
		p.config.TLSClientCert,
		p.config.TLSClientKey,
		p.config.TLSSkipVerify,
	)
}

func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.netListener != nil {
		p.netListener.Close()
		p.netListener = nil
	}

	for _, pool := range p.pools {
		pool.Close()
	}
	p.pools = make(map[[32]byte]*pool.ConnectionPool)
	return nil
}
