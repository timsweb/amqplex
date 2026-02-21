package tests

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/proxy"
)

func TestTLSConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Cleanup(func() {
		os.RemoveAll("test_certs")
	})

	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(caCert, caKey, serverCert, clientCert)
	assert.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15673,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         "test_certs/server.crt",
		TLSKey:          "test_certs/server.key",
		TLSCACert:       "test_certs/ca.crt",
		TLSClientCert:   "test_certs/client.crt",
		TLSClientKey:    "test_certs/client.key",
		TLSSkipVerify:   true,
	}

	p, err := proxy.NewProxy(cfg)
	assert.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	caCertPool := x509.NewCertPool()
	caData, err := os.ReadFile("test_certs/ca.crt")
	assert.NoError(t, err)
	caCertPool.AppendCertsFromPEM(caData)

	clientCert2, err := tls.LoadX509KeyPair("test_certs/client.crt", "test_certs/client.key")
	assert.NoError(t, err)

	tlsConfig := &tls.Config{
		RootCAs:            caCertPool,
		Certificates:       []tls.Certificate{clientCert2},
		InsecureSkipVerify: true,
	}

	conn, err := tls.Dial("tcp", "localhost:15673", tlsConfig)
	assert.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
	assert.NoError(t, err)
}

func TestConnectionMultiplexing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Cleanup(func() {
		os.RemoveAll("test_certs")
	})

	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(caCert, caKey, serverCert, clientCert)
	assert.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15673,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         "test_certs/server.crt",
		TLSKey:          "test_certs/server.key",
		TLSCACert:       "test_certs/ca.crt",
		TLSSkipVerify:   true,
	}

	p, _ := proxy.NewProxy(cfg)
	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	caCertPool := x509.NewCertPool()
	caData, err := os.ReadFile("test_certs/ca.crt")
	assert.NoError(t, err)
	caCertPool.AppendCertsFromPEM(caData)

	var conns []net.Conn
	for i := 0; i < 3; i++ {
		conn, err := tls.Dial("tcp", "localhost:15673", &tls.Config{
			RootCAs:            caCertPool,
			InsecureSkipVerify: true,
		})
		assert.NoError(t, err)
		conns = append(conns, conn)
	}

	for _, conn := range conns {
		conn.Close()
	}

	assert.Equal(t, 3, len(conns))
}
