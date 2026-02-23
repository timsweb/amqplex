package proxy

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/tlsutil"
)

type Proxy struct {
	listener      *AMQPListener
	config        *config.Config
	upstreams     map[[32]byte][]*ManagedUpstream // slice per credential set
	netListener   net.Listener
	mu            sync.RWMutex
	wg            sync.WaitGroup // tracks active handleConnection goroutines
	activeClients atomic.Int32
	logger        *slog.Logger
}

func NewProxy(cfg *config.Config, logger *slog.Logger) (*Proxy, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	listener, err := NewAMQPListener(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	return &Proxy{
		listener:  listener,
		config:    cfg,
		upstreams: make(map[[32]byte][]*ManagedUpstream),
		logger:    logger,
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

	done := make(chan struct{})
	defer close(done) // signals cleanup goroutine when Start returns

	if p.config.PoolIdleTimeout > 0 {
		go p.startIdleCleanup(done)
	}

	p.logger.Info("proxy started",
		slog.String("addr", fmt.Sprintf("%s:%d", p.config.ListenAddress, p.config.ListenPort)),
	)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if isClosedError(err) {
				return nil
			}
			// Transient error: continue accepting
			continue
		}
		// Hold the read lock while calling wg.Add so it cannot race with
		// Stop's write lock + wg.Wait. After Stop sets netListener=nil and
		// releases the write lock, this guard will catch any stale-accepted
		// connections and skip launching a goroutine.
		p.mu.RLock()
		if p.netListener == nil {
			p.mu.RUnlock()
			conn.Close()
			return nil
		}
		if p.config.MaxClientConnections > 0 &&
			int(p.activeClients.Load()) >= p.config.MaxClientConnections {
			p.mu.RUnlock()
			conn.Close()
			continue
		}
		p.wg.Add(1)
		p.activeClients.Add(1)
		p.mu.RUnlock()
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
	for _, m := range p.upstreams[key] {
		if m.HasCapacity() {
			p.mu.RUnlock()
			return m, nil
		}
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check under write lock
	for _, m := range p.upstreams[key] {
		if m.HasCapacity() {
			return m, nil
		}
	}

	// All existing upstreams full (or none exist): check global cap before dialling.
	// Upstreams that are mid-reconnect (stopped=false, no clients) still count —
	// they hold their slot and will return to service once reconnected.
	if p.config.MaxUpstreamConnections > 0 {
		total := 0
		for _, slice := range p.upstreams {
			total += len(slice)
		}
		if total >= p.config.MaxUpstreamConnections {
			return nil, errors.New("upstream connection limit reached")
		}
	}

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
		upstreamAddr:  addr,
		logger:        p.logger,
	}
	m.dialFn = func() (*UpstreamConn, error) {
		nc, err := p.dialUpstream(network, addr)
		if err != nil {
			return nil, err
		}
		return performUpstreamHandshake(nc, username, password, vhost)
	}
	m.Start(upstreamConn)

	p.upstreams[key] = append(p.upstreams[key], m)
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
	defer p.activeClients.Add(-1) // pairs with activeClients.Add(1) in Start()

	cc := NewClientConnection(clientConn, p)

	// Read and validate AMQP protocol header.
	header := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, header); err != nil {
		return
	}
	if string(header) != ProtocolHeader {
		return
	}

	// Client-side handshake: extracts credentials and vhost.
	if err := cc.Handle(); err != nil {
		return
	}

	cc.Mu.RLock()
	username, password, vhost := cc.Username, cc.Password, cc.Vhost
	cc.Mu.RUnlock()
	if username == "" {
		return
	}

	// Acquire or create a ManagedUpstream for this credential set.
	managed, err := p.getOrCreateManagedUpstream(username, password, vhost)
	if err != nil {
		// Log rejection BEFORE any "connected" event — client was never truly connected.
		p.logger.Warn("client rejected — upstream unavailable",
			slog.String("remote_addr", clientConn.RemoteAddr().String()),
			slog.String("error", err.Error()),
		)
		// Upstream unavailable — send Connection.Close 503 to client.
		_ = WriteFrame(cc.Writer, &Frame{
			Type:    FrameTypeMethod,
			Channel: 0,
			Payload: serializeConnectionClose(503, "upstream unavailable"),
		})
		_ = cc.Writer.Flush()
		return
	}

	// Only log "connected" once we have a valid upstream.
	remoteAddr := clientConn.RemoteAddr().String()
	p.logger.Info("client connected",
		slog.String("remote_addr", remoteAddr),
		slog.String("user", username),
		slog.String("vhost", vhost),
	)
	start := time.Now()
	defer func() {
		p.logger.Info("client disconnected",
			slog.String("remote_addr", remoteAddr),
			slog.String("user", username),
			slog.String("vhost", vhost),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	}()

	managed.Register(cc)
	if managed.stopped.Load() {
		// Upstream was removed by idle cleanup between lookup and register.
		managed.Deregister(cc)
		p.logger.Warn("client rejected — upstream stopped during registration",
			slog.String("remote_addr", remoteAddr),
		)
		_ = WriteFrame(cc.Writer, &Frame{
			Type:    FrameTypeMethod,
			Channel: 0,
			Payload: serializeConnectionClose(503, "upstream unavailable"),
		})
		_ = cc.Writer.Flush()
		return
	}
	defer func() {
		managed.Deregister(cc)
		p.releaseClientChannels(managed, cc)
	}()

	// Client → Upstream proxy loop.
	// Upstream → Client is handled by ManagedUpstream.readLoop.
	for {
		frame, err := ParseFrame(cc.Reader)
		if err != nil {
			return
		}

		// Consume client heartbeats — proxy owns the upstream heartbeat.
		if frame.Type == FrameTypeHeartbeat {
			continue
		}

		if isChannelOpen(frame) {
			upstreamID, err := managed.AllocateChannel(frame.Channel, cc)
			if err != nil {
				// No channel capacity — close this client.
				return
			}
			cc.MapChannel(frame.Channel, upstreamID)
		}

		// Remap client channel ID to upstream channel ID.
		cc.Mu.RLock()
		upstreamChanID, mapped := cc.ChannelMapping[frame.Channel]
		cc.Mu.RUnlock()

		remapped := *frame
		if mapped {
			remapped.Channel = upstreamChanID
		}

		// Mark channel unsafe if needed.
		markChannelSafety(frame, cc)

		if err := managed.writeFrameToUpstream(&remapped); err != nil {
			return
		}

		if isChannelClose(frame) {
			cc.Mu.RLock()
			upstreamID := cc.ChannelMapping[frame.Channel]
			cc.Mu.RUnlock()
			managed.ReleaseChannel(upstreamID)
			cc.UnmapChannel(frame.Channel)
		}
	}
}

// releaseClientChannels is called when a client disconnects. Safe channels are
// released for reuse; unsafe channels are closed on the upstream first.
func (p *Proxy) releaseClientChannels(managed *ManagedUpstream, cc *ClientConnection) {
	cc.Mu.RLock()
	channels := make(map[uint16]uint16, len(cc.ChannelMapping))
	for clientID, upstreamID := range cc.ChannelMapping {
		channels[clientID] = upstreamID
	}
	cc.Mu.RUnlock()

	for clientID, upstreamID := range channels {
		cc.Mu.RLock()
		ch, ok := cc.ClientChannels[clientID]
		cc.Mu.RUnlock()

		if ok && !ch.Safe {
			// Send Channel.Close to upstream for unsafe channels.
			closePayload := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 40})
			closePayload = append(closePayload, 0, 0)                       // reply-code = 0
			closePayload = append(closePayload, serializeShortString("")...) // reply-text = ""
			closePayload = append(closePayload, 0, 0, 0, 0)                 // class-id=0, method-id=0
			_ = managed.writeFrameToUpstream(&Frame{
				Type:    FrameTypeMethod,
				Channel: upstreamID,
				Payload: closePayload,
			})
		}

		managed.ReleaseChannel(upstreamID)
		cc.UnmapChannel(clientID)
	}
}

func (p *Proxy) upstreamTLSConfig() (*tls.Config, error) {
	return tlsutil.LoadTLSConfig(
		p.config.TLSCACert,
		p.config.TLSClientCert,
		p.config.TLSClientKey,
		p.config.TLSSkipVerify,
	)
}

// removeIdleUpstreams sweeps all upstreams under write lock and stops any
// that have been idle longer than timeout.
func (p *Proxy) removeIdleUpstreams(timeout time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, upstreams := range p.upstreams {
		kept := make([]*ManagedUpstream, 0, len(upstreams))
		for _, m := range upstreams {
			if m.markStoppedIfIdle(timeout) {
				p.logger.Info("upstream idle timeout",
					slog.String("upstream_addr", m.upstreamAddr),
					slog.String("user", m.username),
					slog.String("vhost", m.vhost),
				)
			} else {
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 {
			delete(p.upstreams, key)
		} else {
			p.upstreams[key] = kept
		}
	}
}

// startIdleCleanup runs on a configurable ticker (PoolCleanupInterval seconds,
// defaulting to 30) and removes idle upstreams. It exits when done is closed.
func (p *Proxy) startIdleCleanup(done <-chan struct{}) {
	timeout := time.Duration(p.config.PoolIdleTimeout) * time.Second
	interval := p.config.PoolCleanupInterval
	if interval <= 0 {
		interval = 30
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.removeIdleUpstreams(timeout)
		case <-done:
			return
		}
	}
}

func (p *Proxy) Stop() error {
	p.mu.Lock()
	if p.netListener != nil {
		p.netListener.Close()
		p.netListener = nil
	}
	for _, upstreams := range p.upstreams {
		for _, m := range upstreams {
			m.stopped.Store(true)
			m.AbortAllClients() // abort client connections so handleConnection exits
			m.mu.Lock()
			if m.conn != nil {
				m.conn.Conn.Close()
			}
			m.mu.Unlock()
		}
	}
	p.upstreams = make(map[[32]byte][]*ManagedUpstream)
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
	p.logger.Info("proxy stopped")
	return nil
}
