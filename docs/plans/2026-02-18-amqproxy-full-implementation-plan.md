# AMQP Proxy Full Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement complete AMQP protocol proxying with TLS support, connection multiplexing, safe channel reuse, channel remapping, and comprehensive testing.

**Architecture:** Parse AMQP frames, proxy them bidirectionally with channel number remapping, manage connection pools per credential set, enforce safe/unsafe channel semantics, support TLS on both ends.

**Tech Stack:** Go 1.23+, amqp091-go for AMQP protocol, crypto/tls for TLS, bufio for efficient I/O, testify for testing.

---

## Task 1: Test Certificate Generation

**Files:**
- Create: `tests/certs.go`

**Step 1: Write test for certificate generation**

```go
package tests

import (
	"crypto/tls"
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestGenerateTestCerts(t *testing.T) {
	caCert, caKey := GenerateTestCA(t)
	assert.NotNil(t, caCert)
	assert.NotNil(t, caKey)

	serverCert, clientCert := GenerateServerCert(t, caCert, caKey), GenerateClientCert(t, caCert, caKey)
	assert.NotNil(t, serverCert)
	assert.NotNil(t, clientCert)

	err := WriteCerts(caCert, caKey, serverCert, clientCert)
	assert.NoError(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/certs_test.go -v`
Expected: FAIL with "GenerateTestCA not defined"

**Step 3: Implement certificate generation**

```go
package tests

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"
)

func GenerateTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NoError(t, err)

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
	assert.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	assert.NoError(t, err)

	return cert, priv
}

func GenerateServerCert(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey) *tls.Certificate {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{Organization: []string{"AMQP Proxy Server"}},
		DNSNames:              []string{"rabbitmq", "localhost"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	assert.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	assert.NoError(t, err)

	return &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: priv}
}

func GenerateClientCert(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey) *tls.Certificate {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{Organization: []string{"AMQP Proxy Client"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	assert.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	assert.NoError(t, err)

	return &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: priv}
}

func WriteCerts(caCert *x509.Certificate, caKey *rsa.PrivateKey, serverCert, clientCert *tls.Certificate) error {
	os.MkdirAll("test_certs", 0755)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)})

	if err := os.WriteFile("test_certs/ca.crt", caPEM.Bytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/ca.key", caKeyPEM.Bytes, 0600); err != nil {
		return err
	}

	serverPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverCert.PrivateKey)})

	if err := os.WriteFile("test_certs/server.crt", serverPEM.Bytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/server.key", serverKeyPEM.Bytes, 0600); err != nil {
		return err
	}

	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Certificate[0]})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientCert.PrivateKey)})

	if err := os.WriteFile("test_certs/client.crt", clientPEM.Bytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile("test_certs/client.key", clientKeyPEM.Bytes, 0600); err != nil {
		return err
	}

	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./tests/certs_test.go -v`
Expected: PASS and test_certs/ directory created

**Step 5: Commit**

```bash
git add tests/certs.go
git commit -m "test: add certificate generation for TLS testing"
```

---

## Task 2: Frame Parser

**Files:**
- Create: `proxy/frame.go`
- Create: `proxy/frame_test.go`

**Step 1: Write failing test for frame parsing**

```go
package proxy

import (
	"bytes"
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestParseFrame(t *testing.T) {
	frame := &Frame{Type: FrameTypeMethod, Channel: 0, Payload: []byte{10, 40}}

	buf := bytes.Buffer{}
	WriteFrame(&buf, frame)

	parsed, err := ParseFrame(&buf)
	assert.NoError(t, err)
	assert.Equal(t, FrameTypeMethod, parsed.Type)
	assert.Equal(t, uint16(0), parsed.Channel)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/frame_test.go -v`
Expected: FAIL with "ParseFrame not defined"

**Step 3: Implement frame parser**

```go
package proxy

import (
	"encoding/binary"
	"errors"
	"io"
)

type FrameType uint8

const (
	FrameTypeMethod   FrameType = 1
	FrameTypeHeader  FrameType = 2
	FrameTypeBody    FrameType = 3
	FrameTypeHeartbeat FrameType = 8
)

type Frame struct {
	Type    FrameType
	Channel uint16
	Payload []byte
}

type MethodHeader struct {
	ClassID  uint16
	MethodID uint16
}

const ProtocolHeader = "AMQP\x00\x00\x09\x01"

func ParseFrame(r io.Reader) (*Frame, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	frameType := FrameType(header[0])
	channel := binary.BigEndian.Uint16(header[1:3])
	size := binary.BigEndian.Uint32(header[3:7])

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	return &Frame{
		Type:    frameType,
		Channel: channel,
		Payload: payload,
	}, nil
}

func WriteFrame(w io.Writer, frame *Frame) error {
	header := make([]byte, 7)
	header[0] = byte(frame.Type)
	binary.BigEndian.PutUint16(header[1:3], frame.Channel)
	binary.BigEndian.PutUint32(header[3:7], uint32(len(frame.Payload)))

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(frame.Payload); err != nil {
		return err
	}
	return nil
}

func ParseMethodHeader(data []byte) (*MethodHeader, error) {
	if len(data) < 4 {
		return nil, errors.New("method header too short")
	}

	return &MethodHeader{
		ClassID:  binary.BigEndian.Uint16(data[0:2]),
		MethodID: binary.BigEndian.Uint16(data[2:4]),
	}, nil
}

func SerializeMethodHeader(h *MethodHeader) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:2], h.ClassID)
	binary.BigEndian.PutUint16(buf[2:4], h.MethodID)
	return buf
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/frame_test.go -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/frame.go proxy/frame_test.go
git commit -m "feat: add AMQP frame parser and serializer"
```

---

## Task 3: Credential Extraction

**CRITICAL NOTE:** This task has been updated to reflect correct AMQP 0-9-1 specification.
Credentials are in **Connection.StartOk** (class 10, method 20), NOT Connection.Open (method 40).
See https://www.rabbitmq.com/amqp-0-9-1-reference.html#connection.start-ok

**Files:**
- Create: `proxy/credentials.go`
- Create: `proxy/credentials_test.go`

**Step 1: Write failing test for credential parsing**

```go
package proxy

import (
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestParseConnectionStartOk(t *testing.T) {
	// PLAIN mechanism format: \0username\0password
	response := serializeConnectionStartOkResponse("user", "pass")
	data := serializeConnectionStartOk("PLAIN", response)

	creds, err := ParseConnectionStartOk(data)
	assert.NoError(t, err)
	assert.Equal(t, "user", creds.Username)
	assert.Equal(t, "pass", creds.Password)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/credentials_test.go -v`
Expected: FAIL with "ParseConnectionStartOk not defined"

**Step 3: Implement credential parsing**

```go
package proxy

import (
	"encoding/binary"
	"errors"
)

type Credentials struct {
	Username string
	Password string
}

func ParseConnectionStartOk(data []byte) (*Credentials, error) {
	header, err := ParseMethodHeader(data)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 20 {
		return nil, errors.New("not a Connection.StartOk frame")
	}

	offset := 4

	// Skip client-properties (table)
	_, tableEnd, err := parseTable(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += tableEnd

	// Skip mechanism (shortstr) - e.g., "PLAIN"
	_, mechLen, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += mechLen

	// Parse response (longstr) - contains \0username\0password
	response, _, err := parseLongString(data[offset:])
	if err != nil {
		return nil, err
	}

	// Parse PLAIN format: \0username\0password
	parts := bytes.Split(response, []byte{0})
	if len(parts) != 3 {
		return nil, errors.New("invalid PLAIN auth format")
	}

	return &Credentials{
		Username: string(parts[1]),
		Password: string(parts[2]),
	}, nil
}

func parseShortString(data []byte) (string, int, error) {
	if len(data) < 1 {
		return "", 0, errors.New("data too short")
	}
	length := int(data[0])
	if len(data) < 1+length {
		return "", 0, errors.New("invalid string length")
	}
	return string(data[1 : 1+length]), 1 + length, nil
}

func parseLongString(data []byte) ([]byte, int, error) {
	if len(data) < 4 {
		return nil, 0, errors.New("data too short")
	}
	length := int(binary.BigEndian.Uint32(data[0:4]))
	if len(data) < 4+length {
		return nil, 0, errors.New("invalid long string length")
	}
	return data[4 : 4+length], 4 + length, nil
}

func parseTable(data []byte) ([]byte, int, error) {
	// For now, skip tables - not needed for credential extraction
	if len(data) < 4 {
		return nil, 0, errors.New("table too short")
	}
	length := int(binary.BigEndian.Uint32(data[0:4]))
	return data[4 : 4+length], 4 + length, nil
}

func serializeConnectionStartOk(mechanism string, response []byte) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 20})

	payload := make([]byte, 0)
	payload = append(payload, serializeEmptyTable()...)
	payload = append(payload, serializeShortString(mechanism)...)
	payload = append(payload, serializeLongString(response)...)
	payload = append(payload, serializeShortString("en_US")...)

	return append(header, payload...)
}

func serializeConnectionStartOkResponse(username, password string) []byte {
	response := []byte{0}
	response = append(response, []byte(username)...)
	response = append(response, 0)
	response = append(response, []byte(password)...)
	return response
}

func serializeEmptyTable() []byte {
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, 0)
	return lengthBytes
}

func serializeLongString(data []byte) []byte {
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, uint32(len(data)))
	result := append(lengthBytes, data...)
	return result
}

func serializeShortString(s string) []byte {
	return append([]byte{byte(len(s))}, s...)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/credentials_test.go -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/credentials.go proxy/credentials_test.go
git commit -m "feat: add AMQP credential parsing (Connection.StartOk)"
```

---

## Task 3.1: Fix Incorrect Connection.Open Implementation

**Context:** Previous implementation incorrectly parsed Connection.Open for credentials.
Per AMQP 0-9-1 spec, credentials are in Connection.StartOk, not Connection.Open.

**Files:**
- Modify: `proxy/credentials.go`
- Modify: `proxy/credentials_test.go`

**Step 1: Deprecate incorrect ParseConnectionOpen**

```go
// ParseConnectionOpen is DEPRECATED - do not use.
// Credentials are extracted from Connection.StartOk, not Connection.Open.
// Connection.Open only contains the vhost to connect to after authentication.
func ParseConnectionOpen(data []byte) (*Credentials, error) {
	return nil, errors.New("ParseConnectionOpen is deprecated. Use ParseConnectionStartOk for credentials.")
}
```

**Step 2: Update tests**

Remove or mark test as deprecated.

**Step 3: Commit**

```bash
git add proxy/credentials.go proxy/credentials_test.go
git commit -m "fix: deprecate incorrect Connection.Open credential parsing"
```

---

## Task 4: Connection Handler (Part 1 - Basic Structure)

**Files:**
- Create: `proxy/connection.go`
- Create: `proxy/connection_test.go`

**Step 1: Write failing test for connection handler**

```go
package proxy

import (
	"net"
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestNewClientConnection(t *testing.T) {
	conn := &net.TCPConn{}
	proxy := &Proxy{}

	clientConn := NewClientConnection(conn, proxy)
	assert.NotNil(t, clientConn)
	assert.Equal(t, conn, clientConn.Conn)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/connection_test.go -v`
Expected: FAIL with "NewClientConnection not defined"

**Step 3: Implement basic connection handler structure**

```go
package proxy

import (
	"net"
	"sync"
)

type ClientConnection struct {
	Conn            net.Conn
	UpstreamConn   *amqp.Connection
	ClientChannels  map[uint16]*ClientChannel
	ChannelMapping  map[uint16]uint16
	Mu              sync.RWMutex
	Proxy           *Proxy
}

type ClientChannel struct {
	ID         uint16
	UpstreamID uint16
	Safe       bool
	Operations map[string]bool
	Mu         sync.RWMutex
	Client     *ClientConnection
}

func NewClientConnection(conn net.Conn, proxy *Proxy) *ClientConnection {
	return &ClientConnection{
		Conn:           conn,
		ClientChannels:  make(map[uint16]*ClientChannel),
		ChannelMapping:  make(map[uint16]uint16),
		Proxy:          proxy,
	}
}

func (cc *ClientConnection) MapChannel(clientID, upstreamID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	cc.ChannelMapping[clientID] = upstreamID

	if channel, ok := cc.ClientChannels[clientID]; ok {
		channel.UpstreamID = upstreamID
	} else {
		cc.ClientChannels[clientID] = &ClientChannel{
			ID:         clientID,
			UpstreamID: upstreamID,
			Safe:       true,
			Operations: make(map[string]bool),
			Client:     cc,
		}
	}
}

func (cc *ClientConnection) UnmapChannel(clientID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	delete(cc.ChannelMapping, clientID)
}

func (cc *ClientConnection) RecordChannelOperation(channelID uint16, operation string, isSafe bool) {
	cc.Mu.RLock()
	channel, ok := cc.ClientChannels[channelID]
	cc.Mu.RUnlock()

	if !ok {
		return
	}

	channel.Mu.Lock()
	defer channel.Mu.Unlock()
	channel.Operations[operation] = true
	if !isSafe {
		channel.Safe = false
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/connection_test.go -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go
git commit -m "feat: add client connection handler structure"
```

---

## Task 5: Frame Proxying Logic

**Files:**
- Create: `proxy/frame_proxy.go`

**Step 1: Write failing test for frame proxying**

```go
package proxy

import (
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestProxyClientToUpstream(t *testing.T) {
	clientConn := &ClientConnection{}
	fp := NewFrameProxy(clientConn)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./proxy/frame_proxy_test.go -v`
Expected: FAIL with "NewFrameProxy not defined"

**Step 3: Implement frame proxying**

```go
package proxy

import (
	"bytes"
)

type FrameProxy struct {
	ClientConn *ClientConnection
	Writer     *bytes.Buffer
}

func NewFrameProxy(clientConn *ClientConnection) *FrameProxy {
	return &FrameProxy{
		ClientConn: clientConn,
		Writer:     bytes.Buffer{},
	}
}

func (fp *FrameProxy) ProxyClientToUpstream(frame *Frame) error {
	fp.ClientConn.Mu.RLock()
	upstreamID, ok := fp.ClientConn.ChannelMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	if !ok {
		return nil // No upstream channel mapped yet, forward as-is
	}

	remapped := *frame
	remapped.Channel = upstreamID

	return WriteFrame(&fp.Writer, &remapped)
}

func (fp *FrameProxy) ProxyUpstreamToClient(frame *Frame) error {
	// Find which client channel maps to this upstream channel
	fp.ClientConn.Mu.RLock()
	var clientID uint16
	for cid, uid := range fp.ClientConn.ChannelMapping {
		if uid == frame.Channel {
			clientID = cid
			break
		}
	}
	fp.ClientConn.Mu.RUnlock()

	if clientID == 0 {
		return nil // No mapping, ignore
	}

	remapped := *frame
	remapped.Channel = clientID

	return WriteFrame(&fp.Writer, &remapped)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./proxy/frame_proxy_test.go -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/frame_proxy.go proxy/frame_proxy_test.go
git commit -m "feat: add frame proxying with channel remapping"
```

---

## Task 6: TLS Listener Support

**Files:**
- Create: `proxy/listener.go`

**Step 1: Update go.mod for amqp091-go**

```bash
go get github.com/rabbitmq/amqp091-go
```

**Step 2: Create TLS listener**

```go
package proxy

import (
	"fmt"
	"net"
	"crypto/tls"
	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/tlsutil"
)

type AMQPListener struct {
	Config     *config.Config
	TLSConfig  *tls.Config
}

func NewAMQPListener(cfg *config.Config) (*AMQPListener, error) {
	var tlsConfig *tls.Config
	var err error

	if cfg.TLSCert != "" && cfg.TLSKey != "" {
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
```

**Step 3: Commit**

```bash
git add proxy/listener.go go.mod go.sum
git commit -m "feat: add TLS listener support and amqp091-go dependency"
```

---

## Task 7: Update Proxy to Use New Components

**Files:**
- Modify: `proxy/proxy.go`

**Step 1: Update Proxy struct and methods**

```go
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/pool"
	"github.com/tim/amqproxy/tlsutil"
)

type Proxy struct {
	listener      *AMQPListener
	config        *config.Config
	pools         map[[32]byte]*pool.ConnectionPool
	mu            sync.RWMutex
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
	listener, err := NewAMQPListener(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	return &Proxy{
		listener: listener,
		config:   cfg,
		pools:    make(map[[32]byte]*pool.ConnectionPool),
	}, nil
}

func (p *Proxy) Start() error {
	var listener net.Listener
	var err error

	if p.config.TLSCert != "" && p.config.TLSKey != "" {
		listener, err = p.listener.StartTLS()
	} else {
		listener, err = p.listener.StartPlain()
	}

	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go p.handleConnection(conn)
	}
}

func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	// Read protocol header
	header := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, header); err != nil {
		return
	}

	if string(header) != ProtocolHeader {
		return
	}

	// Respond with Connection.Start
	// TODO: Implement full connection flow
}
```

**Step 2: Run tests**

Run: `go test ./proxy/... -v`
Expected: PASS

**Step 3: Commit**

```bash
git add proxy/proxy.go
git commit -m "feat: integrate TLS listener into proxy"
```

---

## Task 8: Docker Compose with TLS

**Files:**
- Modify: `docker-compose.yml`

**Step 1: Create TLS-enabled docker-compose**

```yaml
services:
  rabbitmq:
    image: rabbitmq:3-management
    ports:
      - "5671:5671"
      - "15672:15672"
    environment:
      RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS: -rabbit loopback_users []
    volumes:
      - rabbitmq-data:/var/lib/rabbitmq
      - ./test_certs:/etc/rabbitmq/certs:ro

  amqproxy:
    build: .
    ports:
      - "5673:5673"
      - "5674:5674"
    environment:
      - AMQP_UPSTREAM_URL=amqps://rabbitmq:5671
      - AMQP_LISTEN_ADDRESS=0.0.0.0
      - AMQP_LISTEN_PORT=5673
      - AMQP_TLS_ENABLED=true
      - AMQP_TLS_CERT=/app/test_certs/server.crt
      - AMQP_TLS_KEY=/app/test_certs/server.key
      - AMQP_UPSTREAM_TLS_ENABLED=true
      - AMQP_UPSTREAM_CA=/app/test_certs/ca.crt
    volumes:
      - ./test_certs:/app/test_certs:ro
    depends_on:
      - rabbitmq

volumes:
  rabbitmq-data:
```

**Step 2: Create RabbitMQ TLS config**

Create `rabbitmq.conf`:
```
listeners.ssl.default = 5671
ssl_options.cacertfile = /etc/rabbitmq/certs/ca.crt
ssl_options.certfile   = /etc/rabbitmq/certs/server.crt
ssl_options.keyfile    = /etc/rabbitmq/certs/server.key
ssl_options.verify     = verify_peer
ssl_options.fail_if_no_peer_cert = false
```

**Step 3: Commit**

```bash
git add docker-compose.yml rabbitmq.conf
git commit -m "chore: add TLS configuration to docker-compose"
```

---

## Task 9: Integration Test - TLS Connection

**Files:**
- Create: `tests/integration_tls_test.go`

**Step 1: Write TLS connection test**

```go
package tests

import (
	"crypto/tls"
	"net"
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

	// Generate test certs first
	caCert, _ := GenerateTestCA(t)
	serverCert, clientCert := GenerateServerCert(t, caCert, _), GenerateClientCert(t, caCert, _)
	WriteCerts(caCert, nil, serverCert, clientCert)

	// Start proxy
	cfg := &config.Config{
		ListenAddress:  "localhost",
		ListenPort:     15673,
		PoolIdleTimeout: 5,
		TLSCert:       "test_certs/server.crt",
		TLSKey:        "test_certs/server.key",
		TLSCACert:      "test_certs/ca.crt",
	}

	p, err := proxy.NewProxy(cfg)
	assert.NoError(t, err)

	go p.Start()
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)

	// Test TLS connection
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(loadPEM("test_certs/ca.crt"))

	tlsConfig := &tls.Config{
		RootCAs: caCertPool,
		Certificates: []tls.Certificate{*clientCert},
	}

	conn, err := tls.Dial("tcp", "localhost:15673", tlsConfig)
	assert.NoError(t, err)
	defer conn.Close()
}
```

**Step 2: Run test**

Run: `go test -v ./tests/integration_tls_test.go`
Expected: PASS

**Step 3: Commit**

```bash
git add tests/integration_tls_test.go
git commit -m "test: add TLS connection integration test"
```

---

## Task 10: Enhanced Connection Pool

**Files:**
- Modify: `pool/pool.go`

**Step 1: Add safe channel management to pool**

```go
// Add to ConnectionPool struct:
SafeChannels map[uint16]bool  // upstream channel IDs that are safe
LastUsed     time.Time

// Add to NewConnectionPool:
SafeChannels: make(map[uint16]bool),
LastUsed:     time.Now(),

// Add functions:
func (p *ConnectionPool) AddSafeChannel(upstreamID uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.SafeChannels[upstreamID] = true
	p.LastUsed = time.Now()
}

func (p *ConnectionPool) IsSafeChannel(upstreamID uint16) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.SafeChannels[upstreamID]
}

func (p *ConnectionPool) RemoveSafeChannel(upstreamID uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.SafeChannels, upstreamID)
}
```

**Step 2: Update pool tests**

Add test:
```go
func TestSafeChannelManagement(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	pool.AddSafeChannel(5)
	assert.True(t, pool.IsSafeChannel(5))

	pool.RemoveSafeChannel(5)
	assert.False(t, pool.IsSafeChannel(5))
}
```

**Step 3: Run tests**

Run: `go test ./pool/... -v`
Expected: PASS

**Step 4: Commit**

```bash
git add pool/pool.go pool/pool_test.go
git commit -m "feat: add safe channel management to connection pool"
```

---

## Task 11: Full Connection Flow

**CRITICAL NOTE:** This task implements correct AMQP 0-9-1 handshake flow.
Connection flow: Start → StartOk → Secure → SecureOk → Tune → TuneOk → Open → OpenOk.
Credentials are extracted from Connection.StartOk (method 11), NOT Connection.Open (method 40).

**Files:**
- Modify: `proxy/connection.go`
- Modify: `proxy/proxy.go` (to use connection handler)

**Step 1: Implement connection handling logic**

```go
// Add to ClientConnection:
Reader *bufio.Reader
Writer *bufio.Writer

func (cc *ClientConnection) ConnectToUpstream(creds *Credentials) error {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()

	conn, err := amqp.DialTLS(cc.Proxy.config.UpstreamURL, cc.Proxy.config.TLSCACert, cc.Proxy.config.TLSClientCert, cc.Proxy.config.TLSClientKey)
	if err != nil {
		return err
	}

	cc.UpstreamConn = conn
	return nil
}

func (cc *ClientConnection) Handle() error {
	cc.Reader = bufio.NewReader(cc.Conn)
	cc.Writer = bufio.NewWriter(cc.Conn)

	// Send Connection.Start
	if err := cc.sendConnectionStart(); err != nil {
		return err
	}

	// Wait for Connection.StartOk (contains credentials)
	frame, err := ParseFrame(cc.Reader)
	if err != nil {
		return err
	}

	header, err := ParseMethodHeader(frame.Payload)
	if err != nil {
		return err
	}

	var creds *Credentials
	if header.ClassID == 10 && header.MethodID == 11 {
		creds, err = ParseConnectionStartOk(frame.Payload)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("expected Connection.StartOk, got class=%d method=%d", header.ClassID, header.MethodID)
	}

	// Send Connection.Tune
	if err := cc.sendConnectionTune(); err != nil {
		return err
	}

	// Wait for Connection.TuneOk
	frame, err = ParseFrame(cc.Reader)
	if err != nil {
		return err
	}

	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return err
	}

	if header.ClassID != 10 || header.MethodID != 31 {
		return fmt.Errorf("expected Connection.TuneOk, got class=%d method=%d", header.ClassID, header.MethodID)
	}

	// Wait for Connection.Open
	frame, err = ParseFrame(cc.Reader)
	if err != nil {
		return err
	}

	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return err
	}

	if header.ClassID == 10 && header.MethodID == 40 {
		// Get or create pool
		pool := cc.Proxy.getOrCreatePool(creds.Username, creds.Password, "/")

		// Mark upstream connection
		cc.Pool = pool
	}

	// Send Connection.Open-OK
	return WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionOpenOK(),
	})
}

func (cc *ClientConnection) sendConnectionStart() error {
	payload := serializeConnectionStart()
	return WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: payload,
	})
}

func (cc *ClientConnection) sendConnectionTune() error {
	// Default tune parameters: frame max=0 (unlimited), channel max=0 (unlimited), heartbeat=60
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], 0) // channel max
	binary.BigEndian.PutUint32(payload[0:4], 0) // frame max
	payload[4] = 60 // heartbeat (seconds)

	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 30})
	payload = append(header, payload...)

	return WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: payload,
	})
}

func serializeConnectionStart() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 10})
	// Server properties, mechanisms, locales
	// For now, minimal implementation
	return append(header, 0, 0, 0, 0) // empty table, empty longstr for mechanisms, empty longstr for locales
}

func serializeConnectionOpenOK() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 41})
	return append(header, 0) // known-hosts
}
```

**Step 2: Run tests**

Run: `go test ./proxy/... -v`
Expected: May fail due to missing amqp connection, update as needed

**Step 3: Commit**

```bash
git add proxy/connection.go
git commit -m "feat: implement AMQP connection flow"
```

---

## Task 12: Update Proxy Stop Method

**Files:**
- Modify: `proxy/proxy.go`

**Step 1: Update Stop method to cleanup properly**

```go
func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pool := range p.pools {
		pool.mu.Lock()
		for _, conn := range pool.Connections {
			if conn.Connection != nil {
				conn.Connection.Close()
			}
		}
		pool.Connections = nil
		if pool.SafeChannels != nil {
			pool.SafeChannels = make(map[uint16]bool)
		}
		pool.mu.Unlock()
	}

	p.pools = make(map[[32]byte]*pool.ConnectionPool)
	return nil
}
```

**Step 2: Commit**

```bash
git add proxy/proxy.go
git commit -m "feat: update Stop method for proper cleanup"
```

---

## Task 13: Final Integration Tests

**Files:**
- Modify: `tests/integration_tls_test.go`

**Step 1: Add comprehensive integration tests**

```go
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

	// Connect 3 clients with same credentials
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

	// Verify single upstream connection was created
	// TODO: Implement verification
}
```

**Step 2: Run tests**

Run: `go test -v ./tests/...`
Expected: Some tests may fail pending implementation

**Step 3: Commit**

```bash
git add tests/integration_tls_test.go
git commit -m "test: add comprehensive integration tests"
```

---

## Task 14: Build and Verify

**Files:**
- Create: `Makefile`
- Modify: `.gitignore`

**Step 1: Create Makefile**

```makefile
.PHONY: build test clean run

build:
	go build -o bin/amqproxy ./cmd/amqproxy

test:
	go test -v ./...

test-short:
	go test -short -v ./...

integration-test:
	go test -v ./tests/...

clean:
	rm -rf bin/ test_certs/

run:
	go run ./cmd/amqproxy

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down
```

**Step 2: Build binary**

Run: `make build`
Expected: Binary created at `bin/amqproxy`

**Step 3: Run all tests**

Run: `make test`
Expected: All unit tests pass, integration tests may fail

**Step 4: Test with docker-compose**

Run: `docker-compose up --build`
Expected: Services start with TLS

**Step 5: Test TLS connection**

Run:
```bash
# Test certs generation
go test -v ./tests/certs_test.go

# Start services
docker-compose up -d

# Test TLS connection (should see AMQP protocol header after handshake)
echo "AMQP\x00\x00\x09\x01" | openssl s_client -connect localhost:5673 -cert test_certs/client.crt -key test_certs/client.key -CAfile test_certs/ca.crt

# Cleanup
docker-compose down
```

Expected: TLS handshake succeeds

**Step 6: Commit final changes**

```bash
git add Makefile .gitignore
echo "test_certs/" >> .gitignore
git commit -m "chore: add build and testing infrastructure"
```

---

## Next Steps

After implementing all tasks:

1. Complete any remaining integration tests
2. Verify all scenarios pass:
   - TLS handshake both ends
   - Connection multiplexing
   - Safe channel reuse
   - Unsafe channel isolation
   - Channel remapping
   - Idle timeout cleanup
3. Performance testing with load simulation
4. Document deployment options
5. Prepare for production build

## Verification Checklist

- [ ] Test certificates generated successfully
- [ ] Frame parser handles all frame types
- [ ] Credentials extracted correctly
- [ ] Connection handler establishes upstream connections
- [ ] Frame proxying remaps channels correctly
- [ ] TLS listener works on both ends
- [ ] Connection pool manages safe channels
- [ ] Integration tests verify TLS connections
- [ ] Binary builds and runs with docker-compose
- [ ] No memory leaks in long-running tests
