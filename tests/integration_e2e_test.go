//go:build integration

package tests

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/proxy"
)

// TestMain waits for RabbitMQ to be reachable before running the suite.
func TestMain(m *testing.M) {
	if !waitForPort("localhost:5672", 30*time.Second) {
		fmt.Fprintln(os.Stderr, "RabbitMQ not reachable on localhost:5672 after 30s — is Docker Compose up?")
		os.Exit(1)
	}
	m.Run()
}

// waitForPort polls addr until it accepts a TCP connection or timeout elapses.
func waitForPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// startProxy starts a proxy in-process and returns it. The proxy is stopped
// via t.Cleanup. It polls until the proxy's listen port accepts connections.
func startProxy(t *testing.T, cfg *config.Config) *proxy.Proxy {
	t.Helper()
	p, err := proxy.NewProxy(cfg, discardLogger())
	require.NoError(t, err)
	go p.Start()
	t.Cleanup(func() { p.Stop() })

	addr := fmt.Sprintf("localhost:%d", cfg.ListenPort)
	require.True(t, waitForPort(addr, 5*time.Second),
		"proxy did not become ready on %s", addr)
	return p
}

// dialProxy opens an amqp091-go connection through the proxy and registers cleanup.
func dialProxy(t *testing.T, port int) *amqp.Connection {
	t.Helper()
	url := fmt.Sprintf("amqp://guest:guest@localhost:%d/", port)
	var conn *amqp.Connection
	var err error
	for i := 0; i < 5; i++ {
		conn, err = amqp.Dial(url)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err, "failed to dial proxy on port %d", port)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// declareQueue declares a non-durable auto-delete queue and registers cleanup.
func declareQueue(t *testing.T, ch *amqp.Channel, name string) amqp.Queue {
	t.Helper()
	q, err := ch.QueueDeclare(
		name,  // name
		false, // durable
		true,  // auto-delete
		false, // exclusive
		false, // no-wait
		nil,   // args
	)
	require.NoError(t, err)
	t.Cleanup(func() { ch.QueueDelete(name, false, false, false) })
	return q
}

// TestBasicPublishConsume verifies a single producer publishes a message that a
// consumer receives end-to-end through the proxy.
func TestBasicPublishConsume(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15681,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	conn := dialProxy(t, 15681)
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	q := declareQueue(t, ch, "e2e-basic")

	body := []byte("hello from e2e")
	err = ch.Publish("", q.Name, false, false, amqp.Publishing{Body: body})
	require.NoError(t, err)

	msg, ok, err := ch.Get(q.Name, true)
	require.NoError(t, err)
	require.True(t, ok, "expected a message in the queue")
	assert.Equal(t, body, msg.Body)
}

// TestMultipleClientsSharedUpstream opens two separate AMQP connections through
// the proxy (same credentials → same ManagedUpstream) and verifies that channel
// multiplexing works: both clients can publish and consume independently.
func TestMultipleClientsSharedUpstream(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15682,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	conn1 := dialProxy(t, 15682)
	conn2 := dialProxy(t, 15682)

	ch1, err := conn1.Channel()
	require.NoError(t, err)
	defer ch1.Close()

	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	q := declareQueue(t, ch1, "e2e-multi")

	// Both clients publish one message each.
	require.NoError(t, ch1.Publish("", q.Name, false, false, amqp.Publishing{Body: []byte("from client1")}))
	require.NoError(t, ch2.Publish("", q.Name, false, false, amqp.Publishing{Body: []byte("from client2")}))

	// Consume both messages from one channel.
	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch1.Get(q.Name, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["from client1"], "missing message from client1")
	assert.True(t, bodies["from client2"], "missing message from client2")
}

// TestClientReconnect verifies that a client can disconnect and reconnect through
// the proxy and continue publishing and consuming messages.
func TestClientReconnect(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15683,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	queueName := "e2e-reconnect"

	// First connection: publish one message then disconnect.
	{
		var conn *amqp.Connection
		var err error
		for i := 0; i < 5; i++ {
			conn, err = amqp.Dial(fmt.Sprintf("amqp://guest:guest@localhost:%d/", 15683))
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		require.NoError(t, err)
		ch, err := conn.Channel()
		require.NoError(t, err)
		_, err = ch.QueueDeclare(queueName, true, false, false, false, nil) // durable
		require.NoError(t, err)
		require.NoError(t, ch.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("msg1")}))
		conn.Close()
	}

	// Second connection: publish a second message then consume both.
	conn2 := dialProxy(t, 15683)
	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	_, err = ch2.QueueDeclare(queueName, true, false, false, false, nil) // durable, must match
	require.NoError(t, err)
	t.Cleanup(func() { ch2.QueueDelete(queueName, false, false, false) })

	require.NoError(t, ch2.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("msg2")}))

	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch2.Get(queueName, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d after reconnect", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["msg1"])
	assert.True(t, bodies["msg2"])
}

// TestIdleUpstreamCleanup verifies that after all clients disconnect and the
// idle timeout elapses, the proxy cleans up the upstream connection and
// successfully creates a new one when a client reconnects.
func TestIdleUpstreamCleanup(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:       "localhost",
		ListenPort:          15684,
		UpstreamURL:         "amqp://localhost:5672",
		PoolIdleTimeout:     1, // 1 second idle timeout
		PoolCleanupInterval: 1, // 1 second ticker so we don't wait 30s
		PoolMaxChannels:     65535,
	}
	startProxy(t, cfg)

	queueName := "e2e-idle-cleanup"

	// First connection: declare a durable queue, publish, then disconnect.
	{
		var conn *amqp.Connection
		var err error
		for i := 0; i < 5; i++ {
			conn, err = amqp.Dial(fmt.Sprintf("amqp://guest:guest@localhost:%d/", 15684))
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		require.NoError(t, err)
		ch, err := conn.Channel()
		require.NoError(t, err)
		_, err = ch.QueueDeclare(queueName, true, false, false, false, nil)
		require.NoError(t, err)
		require.NoError(t, ch.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("before-cleanup")}))
		conn.Close()
	}

	// Wait for idle cleanup to fire (timeout=1s, ticker=1s, so ~2s is sufficient).
	time.Sleep(3 * time.Second)

	// Reconnect — this must create a new upstream connection after cleanup.
	conn2 := dialProxy(t, 15684)
	t.Cleanup(func() { conn2.Close() })
	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	_, err = ch2.QueueDeclare(queueName, true, false, false, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ch2.QueueDelete(queueName, false, false, false) })

	// Publish a second message and consume both — verifies full round-trip.
	require.NoError(t, ch2.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("after-cleanup")}))

	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch2.Get(queueName, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d after idle cleanup", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["before-cleanup"])
	assert.True(t, bodies["after-cleanup"])
}
