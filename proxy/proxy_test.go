package proxy

import (
	"github.com/stretchr/testify/assert"
	"github.com/tim/amqproxy/config"
	"testing"
)

func TestNewProxy(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      5673,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	proxy, err := NewProxy(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, proxy)
	assert.NotNil(t, proxy.listener)
}
