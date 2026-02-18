package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func LoadTLSConfig(caCertPath, clientCertPath, clientKeyPath string, skipVerify bool) (*tls.Config, error) {
	config := &tls.Config{
		InsecureSkipVerify: skipVerify,
	}

	// Load server CA certificate
	if caCertPath != "" {
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, err
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		config.RootCAs = caCertPool
	} else {
		// Use system certificate store
		config.RootCAs, _ = x509.SystemCertPool()
	}

	// Load client certificate for mTLS (optional)
	if clientCertPath != "" || clientKeyPath != "" {
		if clientCertPath == "" || clientKeyPath == "" {
			return nil, fmt.Errorf("both client_cert and client_key must be provided for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return config, nil
}
