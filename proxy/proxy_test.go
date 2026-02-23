package proxy

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqproxy/config"
)

func TestNewProxy(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      5673,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	proxy, err := NewProxy(cfg, discardLogger())
	assert.NoError(t, err)
	assert.NotNil(t, proxy)
	assert.NotNil(t, proxy.listener)
}

func TestGetOrCreateManagedUpstream_ReturnsSameInstance(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15680,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Without a real upstream, we can only test that the map key logic works.
	// We test this by patching the upstreams map directly.
	key := p.getPoolKey("user", "pass", "/")
	existing := &ManagedUpstream{
		username:      "user",
		password:      "pass",
		vhost:         "/",
		maxChannels:   65535,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		dialFn:        func() (*UpstreamConn, error) { return nil, nil },
	}
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{existing}
	p.mu.Unlock()

	// Should return the same instance since it has capacity
	m, err := p.getOrCreateManagedUpstream("user", "pass", "/")
	require.NoError(t, err)
	assert.Same(t, existing, m)

	// Different credentials should not match
	key2 := p.getPoolKey("other", "pass", "/")
	existing2 := &ManagedUpstream{
		username:      "other",
		password:      "pass",
		vhost:         "/",
		maxChannels:   65535,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		dialFn:        func() (*UpstreamConn, error) { return nil, nil },
	}
	p.mu.Lock()
	p.upstreams[key2] = []*ManagedUpstream{existing2}
	p.mu.Unlock()

	m2, err := p.getOrCreateManagedUpstream("other", "pass", "/")
	require.NoError(t, err)
	assert.Same(t, existing2, m2)
	assert.NotSame(t, m, m2)
}

func TestProxyLogsStartAndStop(t *testing.T) {
	lc, logger := newCapture()
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15690,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}
	p, err := NewProxy(cfg, logger)
	require.NoError(t, err)

	started := make(chan struct{})
	go func() {
		close(started)
		_ = p.Start()
	}()
	<-started
	require.True(t, lc.waitForMessage("proxy started", 500*time.Millisecond), "expected 'proxy started' log")
	addrVal, ok := lc.attrValue("addr")
	assert.True(t, ok, "expected addr field in 'proxy started' log")
	assert.Contains(t, addrVal.String(), "15690")

	_ = p.Stop()
	assert.Contains(t, lc.messages(), "proxy stopped")
}

func TestClientRejectedWhenUpstreamUnavailable(t *testing.T) {
	lc, logger := newCapture()
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15692,
		UpstreamURL:     "amqp://localhost:19999", // nothing listening there
		PoolIdleTimeout: 5,
	}
	p, err := NewProxy(cfg, logger)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// Connect a raw TCP client and complete the full AMQP handshake so the
	// proxy reaches the upstream dial step and fails.
	conn, err := net.Dial("tcp", "localhost:15692")
	require.NoError(t, err)
	defer conn.Close()

	// 1. Send AMQP protocol header
	_, err = conn.Write([]byte(ProtocolHeader))
	require.NoError(t, err)

	// 2. Read Connection.Start from the proxy
	r := bufio.NewReader(conn)
	_, err = ParseFrame(r)
	require.NoError(t, err)

	// 3. Send Connection.Start-OK with credentials
	startOkPayload := serializeConnectionStartOk("PLAIN",
		serializeConnectionStartOkResponse("guest", "guest"))
	err = WriteFrame(conn, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: startOkPayload,
	})
	require.NoError(t, err)

	// 4. Read Connection.Tune from the proxy
	_, err = ParseFrame(r)
	require.NoError(t, err)

	// 5. Send Connection.Tune-OK
	tuneOkPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 31})
	tuneOkPayload = append(tuneOkPayload,
		0, 0, // channel-max
		0, 2, 0, 0, // frame-max (128KB)
		0, 60, // heartbeat
	)
	err = WriteFrame(conn, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: tuneOkPayload,
	})
	require.NoError(t, err)

	// 6. Send Connection.Open with vhost "/"
	openPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	openPayload = append(openPayload, serializeShortString("/")...)
	openPayload = append(openPayload, 0) // capabilities (shortstr, empty)
	openPayload = append(openPayload, 0) // insist = false
	err = WriteFrame(conn, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: openPayload,
	})
	require.NoError(t, err)

	// The proxy will log "client rejected — upstream unavailable" after completing
	// the client-side handshake and failing to dial the (non-existent) upstream.
	assert.True(t, lc.waitForMessage("client rejected — upstream unavailable", 2*time.Second),
		"expected 'client rejected — upstream unavailable' log, got: %v", lc.messages())
}
