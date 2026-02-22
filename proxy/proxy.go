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
	"time"

	"github.com/timsweb/amqproxy/config"
	"github.com/timsweb/amqproxy/tlsutil"
)

type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	upstreams   map[[32]byte]*ManagedUpstream
	netListener net.Listener
	mu          sync.RWMutex
	wg          sync.WaitGroup // tracks active handleConnection goroutines
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
	listener, err := NewAMQPListener(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	return &Proxy{
		listener:  listener,
		config:    cfg,
		upstreams: make(map[[32]byte]*ManagedUpstream),
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
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConnection(conn)
		}()
	}
}

func isClosedError(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

func (p *Proxy) getPoolKey(username, password, vhost string) [32]byte {
	credentials := fmt.Sprintf("%s:%s:%s", username, password, vhost)
	return sha256.Sum256([]byte(credentials))
}

func (p *Proxy) getOrCreateManagedUpstream(username, password, vhost string) (*ManagedUpstream, error) {
	key := p.getPoolKey(username, password, vhost)

	p.mu.RLock()
	existing, ok := p.upstreams[key]
	p.mu.RUnlock()
	if ok && existing.HasCapacity() {
		return existing, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check under write lock
	if existing, ok := p.upstreams[key]; ok && existing.HasCapacity() {
		return existing, nil
	}

	// Dial new upstream
	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
	if err != nil {
		return nil, err
	}

	netConn, err := p.dialUpstream(network, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream: %w", err)
	}

	upstreamConn, err := performUpstreamHandshake(netConn, username, password, vhost)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("upstream handshake failed: %w", err)
	}

	m := &ManagedUpstream{
		username:      username,
		password:      password,
		vhost:         vhost,
		maxChannels:   uint16(p.config.PoolMaxChannels),
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
	}
	m.dialFn = func() (*UpstreamConn, error) {
		nc, err := p.dialUpstream(network, addr)
		if err != nil {
			return nil, err
		}
		return performUpstreamHandshake(nc, username, password, vhost)
	}
	m.Start(upstreamConn)

	p.upstreams[key] = m
	return m, nil
}

func (p *Proxy) dialUpstream(network, addr string) (net.Conn, error) {
	if network == "tcp+tls" {
		tlsCfg, err := p.upstreamTLSConfig()
		if err != nil {
			return nil, err
		}
		return tls.Dial("tcp", addr, tlsCfg)
	}
	return net.Dial("tcp", addr)
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

	// Step 3: Acquire ManagedUpstream for these credentials.
	_, err := p.getOrCreateManagedUpstream(connPool.Username, connPool.Password, connPool.Vhost)
	if err != nil {
		return
	}

	// Step 4: Establish upstream connection (legacy path; full rewrite is Task 6).
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

	// Step 5: Bidirectional frame proxy.
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
	if p.netListener != nil {
		p.netListener.Close()
		p.netListener = nil
	}
	for _, m := range p.upstreams {
		m.stopped.Store(true)
		m.mu.Lock()
		if m.conn != nil {
			m.conn.Conn.Close()
		}
		m.mu.Unlock()
	}
	p.upstreams = make(map[[32]byte]*ManagedUpstream)
	p.mu.Unlock()

	// Wait up to 30 seconds for active handleConnection goroutines to exit.
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
	return nil
}
