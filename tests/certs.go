package tests

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"time"
)

func GenerateTestCA() (*x509.Certificate, *rsa.PrivateKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"AMQP Proxy Test"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		panic(err)
	}

	return cert, priv
}

func GenerateServerCert(caCert *x509.Certificate, caKey *rsa.PrivateKey) *tls.Certificate {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{Organization: []string{"AMQP Proxy Server"}},
		DNSNames:     []string{"rabbitmq", "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		panic(err)
	}

	_, err = x509.ParseCertificate(certDER)
	if err != nil {
		panic(err)
	}

	return &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: priv}
}

func GenerateClientCert(caCert *x509.Certificate, caKey *rsa.PrivateKey) *tls.Certificate {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{Organization: []string{"AMQP Proxy Client"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		panic(err)
	}

	_, err = x509.ParseCertificate(certDER)
	if err != nil {
		panic(err)
	}

	return &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: priv}
}

func WriteCerts(caCert *x509.Certificate, caKey *rsa.PrivateKey, serverCert *tls.Certificate, clientCert *tls.Certificate) error {
	if err := os.MkdirAll("test_certs", 0755); err != nil {
		return err
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	caKeyBytes, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return err
	}
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: caKeyBytes})

	if err := os.WriteFile("test_certs/ca.crt", caPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/ca.key", caKeyPEM, 0600); err != nil {
		return err
	}

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]})
	serverCertKeyBytes, err := x509.MarshalPKCS8PrivateKey(serverCert.PrivateKey)
	if err != nil {
		return err
	}
	serverCertKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: serverCertKeyBytes})

	if err := os.WriteFile("test_certs/server.crt", serverCertPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/server.key", serverCertKeyPEM, 0600); err != nil {
		return err
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Certificate[0]})
	clientCertKeyBytes, err := x509.MarshalPKCS8PrivateKey(clientCert.PrivateKey)
	if err != nil {
		return err
	}
	clientCertKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: clientCertKeyBytes})

	if err := os.WriteFile("test_certs/client.crt", clientCertPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/client.key", clientCertKeyPEM, 0600); err != nil {
		return err
	}

	return nil
}
