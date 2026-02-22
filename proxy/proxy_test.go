package proxy

import (
	"testing"

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

	proxy, err := NewProxy(cfg)
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
	p, err := NewProxy(cfg)
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
