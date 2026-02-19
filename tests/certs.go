package tests

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
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

	cert, err := x509.ParseCertificate(certDER)
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

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		panic(err)
	}

	return &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: priv}
}

func WriteCerts(caCert *x509.Certificate, caKey *rsa.PrivateKey, serverCert *tls.Certificate, clientCert *tls.Certificate) error {
	os.MkdirAll("test_certs", 0755)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS8PrivateKey(caKey)})

	if err := os.WriteFile("test_certs/ca.crt", caPEM.Bytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/ca.key", caKeyPEM.Bytes, 0600); err != nil {
		return err
	}

	serverCertBytes, _ := x509.MarshalPKCS8PrivateKey(serverCert.PrivateKey)
	if err := os.WriteFile("test_certs/server.crt", serverCertBytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/server.key", serverCertBytes, 0600); err != nil {
		return err
	}

	clientCertBytes, _ := x509.MarshalPKCS8PrivateKey(clientCert.PrivateKey)
	if err := os.WriteFile("test_certs/client.crt", clientCertBytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/client.key", clientCertBytes, 0600); err != nil {
		return err
	}

	return nil
}
