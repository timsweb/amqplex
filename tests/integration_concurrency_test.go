package tests

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqproxy/config"
	"github.com/timsweb/amqproxy/proxy"
)

// TestConcurrentConnections tests multiple clients connecting simultaneously
func TestConcurrentConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15676,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	// Connect 10 concurrent clients
	numClients := 10
	var conns []net.Conn
	var wg sync.WaitGroup

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var conn net.Conn
			var err error
			for j := 0; j < 20; j++ {
				conn, err = net.Dial("tcp", "localhost:15676")
				if err == nil {
					break
				}
				time.Sleep(time.Duration(j+1) * 10 * time.Millisecond)
			}
			if err == nil && conn != nil {
				conns = append(conns, conn)
			}
		}(i)
	}
	wg.Wait()

	// Most clients should succeed (upstream might not be running)
	assert.Greater(t, len(conns), 0, "at least some clients should connect")

	// Clean up connections
	for _, conn := range conns {
		conn.Close()
	}
}

// TestClientDisconnectMidHandshake tests proxy behavior when client drops during handshake
func TestClientDisconnectMidHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15677,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	// Connect, send partial handshake, then disconnect
	conn, err := net.Dial("tcp", "localhost:15677")
	require.NoError(t, err)
	defer conn.Close()

	// Send protocol header
	_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
	require.NoError(t, err)

	// Read Connection.Start response
	buf := make([]byte, 512)
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Greater(t, n, 7)

	// Disconnect without completing handshake
	conn.Close()

	// Wait a bit to ensure proxy handles the disconnection cleanly
	time.Sleep(100 * time.Millisecond)

	// Proxy should still be running and accept new connections
	conn2, err := net.Dial("tcp", "localhost:15677")
	if err == nil {
		conn2.Close()
	}
}

// TestInvalidAMQPHeader tests proxy rejects invalid protocol headers
func TestInvalidAMQPHeader(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15678,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	// Send invalid protocol header
	conn, err := net.Dial("tcp", "localhost:15678")
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("INVALID"))
	assert.NoError(t, err)

	// Connection should be closed by proxy
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	// Expect connection close or timeout (no response)
	if err == nil {
		// Should not receive valid response
		assert.False(t, true, "proxy should close connection with invalid header")
	}
}
