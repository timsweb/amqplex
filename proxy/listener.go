package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"github.com/timsweb/amqplex/config"
)

type AMQPListener struct {
	Config    *config.Config
	TLSConfig *tls.Config
}

func NewAMQPListener(cfg *config.Config) (*AMQPListener, error) {
	var tlsConfig *tls.Config

	// Load server TLS certificates if provided
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load server TLS cert/key pair: %w", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
	}

	// Load CA certificate for client verification (optional)
	if cfg.TLSCACert != "" {
		caCert, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM([]byte(caCert)) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		}
		tlsConfig.ClientCAs = caCertPool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return &AMQPListener{
		Config:    cfg,
		TLSConfig: tlsConfig,
	}, nil
}

func (l *AMQPListener) StartPlain() (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", l.Config.ListenAddress, l.Config.ListenPort)
	return net.Listen("tcp", addr)
}

func (l *AMQPListener) StartTLS() (net.Listener, error) {
	if l.TLSConfig == nil {
		return nil, fmt.Errorf("TLS config not available")
	}
	addr := fmt.Sprintf("%s:%d", l.Config.ListenAddress, l.Config.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	return tls.NewListener(listener, l.TLSConfig), nil
}
