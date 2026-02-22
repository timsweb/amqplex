package tests

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqproxy/config"
	"github.com/timsweb/amqproxy/proxy"
)

func TestProxyIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15673,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	var conn net.Conn
	for i := 0; i < 10; i++ {
		conn, err = net.Dial("tcp", "localhost:15673")
		if err == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	require.NoError(t, err)
	defer conn.Close()

	// Send AMQP protocol header
	_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
	require.NoError(t, err)

	// Expect Connection.Start response within 2 seconds
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Greater(t, n, 7)

	// Frame type = 1 (method frame)
	assert.Equal(t, byte(1), buf[0])
	// Channel = 0
	assert.Equal(t, byte(0), buf[1])
	assert.Equal(t, byte(0), buf[2])
	// Method: class=10 (connection), method=10 (start)
	assert.Equal(t, byte(0), buf[7])
	assert.Equal(t, byte(10), buf[8])
	assert.Equal(t, byte(0), buf[9])
	assert.Equal(t, byte(10), buf[10])
}
