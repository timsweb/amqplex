package tests

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqproxy/config"
	"github.com/timsweb/amqproxy/proxy"
)

func TestTLSConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	certDir := t.TempDir()

	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(certDir, caCert, caKey, serverCert, clientCert)
	require.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15674,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         filepath.Join(certDir, "server.crt"),
		TLSKey:          filepath.Join(certDir, "server.key"),
		// No TLSCACert — not requiring client certs for this test
	}

	p, err := proxy.NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	caCertPool := x509.NewCertPool()
	caData, err := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	require.NoError(t, err)
	caCertPool.AppendCertsFromPEM(caData)

	tlsDialCfg := &tls.Config{RootCAs: caCertPool}

	var conn net.Conn
	for i := 0; i < 10; i++ {
		conn, err = tls.Dial("tcp", "localhost:15674", tlsDialCfg)
		if err == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	require.NoError(t, err, "failed to connect to TLS proxy after retries")
	require.NotNil(t, conn)
	defer conn.Close()

	// Send AMQP protocol header — proxy should respond with Connection.Start
	_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
	require.NoError(t, err)

	// Read response frame — expect Connection.Start (class=10, method=10)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Greater(t, n, 7, "expected a full frame")
	// Frame type=1 (method), channel=0
	assert.Equal(t, byte(1), buf[0])
	// After 7-byte frame header: method class=10, method=10
	assert.Equal(t, byte(0), buf[7])
	assert.Equal(t, byte(10), buf[8])
	assert.Equal(t, byte(0), buf[9])
	assert.Equal(t, byte(10), buf[10])
}

func TestConnectionMultiplexing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	certDir := t.TempDir()
	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(certDir, caCert, caKey, serverCert, clientCert)
	require.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15675,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         filepath.Join(certDir, "server.crt"),
		TLSKey:          filepath.Join(certDir, "server.key"),
	}

	p, _ := proxy.NewProxy(cfg, discardLogger())
	go p.Start()
	defer p.Stop()

	caCertPool := x509.NewCertPool()
	caData, _ := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	caCertPool.AppendCertsFromPEM(caData)

	tlsDialCfg := &tls.Config{RootCAs: caCertPool}

	// Connect 3 clients, each sends the protocol header, each must receive Connection.Start
	for i := 0; i < 3; i++ {
		var conn net.Conn
		for j := 0; j < 10; j++ {
			conn, err = tls.Dial("tcp", "localhost:15675", tlsDialCfg)
			if err == nil {
				break
			}
			time.Sleep(time.Duration(j+1) * 50 * time.Millisecond)
		}
		require.NoError(t, err, "client %d failed to connect", i)
		defer conn.Close()

		_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
		require.NoError(t, err)

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		require.Greater(t, n, 7, "client %d got no response", i)
		assert.Equal(t, byte(1), buf[0], "client %d: expected method frame", i)
	}
}
