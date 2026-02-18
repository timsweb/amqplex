package tests

import (
	"github.com/stretchr/testify/assert"
	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/proxy"
	"net"
	"testing"
	"time"
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
		TLSSkipVerify:   true,
	}

	p, err := proxy.NewProxy(cfg)
	assert.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	// Test that proxy is listening
	conn, err := net.Dial("tcp", "localhost:15673")
	assert.NoError(t, err)
	defer conn.Close()
}
