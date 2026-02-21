package proxy

import (
	"crypto/tls"
	"fmt"
	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/tlsutil"
	"net"
)

type AMQPListener struct {
	Config    *config.Config
	TLSConfig *tls.Config
}

func NewAMQPListener(cfg *config.Config) (*AMQPListener, error) {
	var tlsConfig *tls.Config
	var err error

	if cfg.TLSCACert != "" || cfg.TLSClientCert != "" || cfg.TLSClientKey != "" {
		tlsConfig, err = tlsutil.LoadTLSConfig(
			cfg.TLSCACert,
			cfg.TLSClientCert,
			cfg.TLSClientKey,
			cfg.TLSSkipVerify,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS config: %w", err)
		}
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
