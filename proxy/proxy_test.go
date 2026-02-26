package proxy

import (
	"bufio"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
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

func TestIdleUpstreamRemovedAfterTimeout(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15695,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 1, // 1 second for test speed
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Pre-populate an upstream that has been idle > timeout
	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	m.lastEmptyTime = time.Now().Add(-2 * time.Second)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	// Run cleanup with 1s timeout
	p.removeIdleUpstreams(time.Second)

	p.mu.RLock()
	remaining := p.upstreams[key]
	p.mu.RUnlock()

	assert.Empty(t, remaining, "idle upstream should have been removed")
	assert.True(t, m.stopped.Load(), "idle upstream should be marked stopped")
}

func TestActiveUpstreamNotRemoved(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15696,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 1,
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	// upstream has a registered client
	client := newStubClient()
	m.Register(client)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	p.removeIdleUpstreams(time.Second)

	p.mu.RLock()
	remaining := p.upstreams[key]
	p.mu.RUnlock()
	assert.Len(t, remaining, 1, "active upstream should not be removed")
	assert.False(t, m.stopped.Load())
}

func TestClientLimitRejectsOverLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15710,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 1,
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// Simulate one active client already occupying the single slot.
	p.activeClients.Add(1)
	defer p.activeClients.Add(-1)

	conn, err := net.Dial("tcp", "localhost:15710")
	require.NoError(t, err)
	defer conn.Close()

	// Proxy should close the connection immediately — any read returns EOF or error.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.Error(t, err, "over-limit connection should be closed immediately")
}

func TestClientLimitAllowsUnderLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15711,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 2,
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// One active client — still one slot free.
	p.activeClients.Add(1)
	defer p.activeClients.Add(-1)

	conn, err := net.Dial("tcp", "localhost:15711")
	require.NoError(t, err)
	defer conn.Close()

	// Proxy should accept the connection and begin the AMQP handshake.
	_, err = conn.Write([]byte(ProtocolHeader))
	require.NoError(t, err)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	n, _ := conn.Read(buf)
	assert.Greater(t, n, 0, "proxy should respond when under limit")
}

func TestClientLimitZeroMeansUnlimited(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15712,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 0, // unlimited
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// Crank the counter to a large value — with limit=0 it must be ignored.
	p.activeClients.Add(1000)
	defer p.activeClients.Add(-1000)

	conn, err := net.Dial("tcp", "localhost:15712")
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte(ProtocolHeader))
	require.NoError(t, err)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	n, _ := conn.Read(buf)
	assert.Greater(t, n, 0, "proxy should respond when limit is 0")
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

func TestUpstreamLimitRejectsNewDial(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15713,
		UpstreamURL:            "amqp://localhost:5672",
		MaxUpstreamConnections: 1,
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Pre-populate one upstream — exactly at the limit.
	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	// Use a different credential set so the re-check under write lock finds no
	// matching upstream and falls through to the limit check.
	// Requesting a different credential set should fail with the limit error,
	// not a dial error.
	_, err = p.getOrCreateManagedUpstream("other", "pass", "/other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream connection limit reached")
}

func TestUpstreamLimitAllowsUnderLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15714,
		UpstreamURL:            "amqp://localhost:19999", // nothing listening
		MaxUpstreamConnections: 2,
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// One upstream in the map — still one slot free.
	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	_, err = p.getOrCreateManagedUpstream("other", "pass", "/other")
	// Must fail with a dial error (nothing listening), NOT a limit error.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "connection limit reached")
}

func TestUpstreamLimitZeroMeansUnlimited(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15715,
		UpstreamURL:            "amqp://localhost:19999",
		MaxUpstreamConnections: 0, // unlimited
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Pre-populate many upstreams — with limit=0 none of this should matter.
	p.mu.Lock()
	for i := 0; i < 100; i++ {
		key := p.getPoolKey(fmt.Sprintf("user%d", i), "pass", "/")
		p.upstreams[key] = []*ManagedUpstream{newTestManagedUpstream(65535)}
	}
	p.mu.Unlock()

	_, err = p.getOrCreateManagedUpstream("newuser", "pass", "/")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "connection limit reached")
}

func TestGetPoolKey_SameCredsProduceSameKey(t *testing.T) {
	p := &Proxy{}
	k1 := p.getPoolKey("user", "pass", "/")
	k2 := p.getPoolKey("user", "pass", "/")
	assert.Equal(t, k1, k2)
}

func TestGetPoolKey_DifferentCredsProduceDifferentKeys(t *testing.T) {
	p := &Proxy{}
	assert.NotEqual(t, p.getPoolKey("a", "b", "/"), p.getPoolKey("a", "b", "/v"))
	assert.NotEqual(t, p.getPoolKey("a", "b", "/"), p.getPoolKey("x", "b", "/"))
	assert.NotEqual(t, p.getPoolKey("aaa", "bbb", "/"), p.getPoolKey("aa", "abbb", "/"))
}
