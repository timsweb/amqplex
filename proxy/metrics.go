package proxy

import (
	"fmt"
	"net/http"
)

// MetricsHandler returns an http.Handler that serves proxy metrics in Prometheus
// text format (compatible with OpenMetrics and the New Relic Infrastructure
// agent's Prometheus integration).
//
// Metrics are collected on each scrape under a read lock — no background
// goroutine, no caching. At typical scrape intervals (15–60s) the contention
// is negligible.
func (p *Proxy) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			activeClients        int
			upstreamTotal        int
			upstreamReconnecting int
			channelsUsed         int
			channelsPendingClose int
			reconnectAttempts    int64
		)

		// Collect all metrics under p.mu so the snapshot is coherent.
		// m.mu (plain sync.Mutex, no RLock) is acquired per-upstream to read
		// conn, usedChannels, and pendingClose.
		p.mu.RLock()
		activeClients = int(p.activeClients.Load())
		for _, upstreams := range p.upstreams {
			for _, m := range upstreams {
				upstreamTotal++
				m.mu.Lock() // sync.Mutex (no RLock); brief hold to snapshot three fields
				if m.conn == nil {
					upstreamReconnecting++
				}
				channelsUsed += len(m.usedChannels)
				channelsPendingClose += len(m.pendingClose)
				m.mu.Unlock()
				reconnectAttempts += m.reconnectTotal.Load()
			}
		}
		p.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		fmt.Fprintf(w, "# HELP amqproxy_active_clients Current number of connected clients.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_active_clients gauge\n")
		fmt.Fprintf(w, "amqproxy_active_clients %d\n", activeClients)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_connections Total upstream AMQP connections.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_connections gauge\n")
		fmt.Fprintf(w, "amqproxy_upstream_connections %d\n", upstreamTotal)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_reconnecting Upstream connections currently in reconnect loop.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_reconnecting gauge\n")
		fmt.Fprintf(w, "amqproxy_upstream_reconnecting %d\n", upstreamReconnecting)

		fmt.Fprintf(w, "# HELP amqproxy_channels_used Total AMQP channels currently allocated.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_channels_used gauge\n")
		fmt.Fprintf(w, "amqproxy_channels_used %d\n", channelsUsed)

		fmt.Fprintf(w, "# HELP amqproxy_channels_pending_close Channels awaiting Channel.CloseOk from broker.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_channels_pending_close gauge\n")
		fmt.Fprintf(w, "amqproxy_channels_pending_close %d\n", channelsPendingClose)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_reconnect_attempts_total Cumulative upstream reconnect attempts since proxy start.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_reconnect_attempts_total counter\n")
		fmt.Fprintf(w, "amqproxy_upstream_reconnect_attempts_total %d\n", reconnectAttempts)
	})
}
