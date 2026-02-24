package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timsweb/amqplex/config"
)

func TestMetricsHandler(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15740,
		UpstreamURL:     "amqp://localhost:5672",
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Set known state.
	p.activeClients.Store(3)

	key := p.getPoolKey("guest", "guest", "/")

	// Upstream 1: connected (conn != nil), 2 used channels, 1 pending close, 5 reconnect attempts.
	m1 := newTestManagedUpstream(65535)
	m1.conn = &UpstreamConn{} // non-nil = connected
	m1.usedChannels[1] = true
	m1.usedChannels[2] = true
	m1.pendingClose[3] = true
	m1.reconnectTotal.Store(5)

	// Upstream 2: reconnecting (conn == nil by default), 2 reconnect attempts.
	m2 := newTestManagedUpstream(65535)
	m2.reconnectTotal.Store(2)

	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m1, m2}
	p.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	p.MetricsHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")

	body := rec.Body.String()
	assert.Contains(t, body, "\namqproxy_active_clients 3\n")
	assert.Contains(t, body, "\namqproxy_upstream_connections 2\n")
	assert.Contains(t, body, "\namqproxy_upstream_reconnecting 1\n")
	assert.Contains(t, body, "\namqproxy_channels_used 2\n")
	assert.Contains(t, body, "\namqproxy_channels_pending_close 1\n")
	assert.Contains(t, body, "\namqproxy_upstream_reconnect_attempts_total 7\n")
}
