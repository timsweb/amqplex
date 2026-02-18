# AMQP Proxy Full Implementation Design

**Goal:** Design a complete AMQP proxy that supports TLS on both ends, multiplexes clients onto upstream connections, implements safe channel reuse, and handles full AMQP protocol frame parsing and proxying.

**Architecture:**
The proxy acts as a transparent AMQP protocol intermediary that:
1. Listens for client AMQP connections (optionally with TLS)
2. Parses AMQP protocol headers, class, method, and content frames
3. Establishes and pools connections to upstream RabbitMQ (with TLS)
4. Remaps channel numbers bidirectionally between client and upstream
5. Enforces safe channel reuse (only Basic.Publish/Get channels are pooled)
6. Closes unsafe channels (those with subscriptions) when client disconnects
7. Implements idle timeout cleanup for pooled connections

**Tech Stack:**
- Go 1.23+, amqp091-go for AMQP protocol, crypto/tls for TLS, testify for testing

---

## AMQP Protocol Overview

AMQP 0-9-1 protocol uses frames with this structure:

```
Frame = Header + Payload
Header = Type (1 byte) + Channel (2 bytes) + Size (4 bytes)
Type: 1=Method, 2=Header, 3=Body, 8=Heartbeat
Channel: 0-65535 (channel ID within connection)
Size: payload size in bytes
```

### Frame Types

**Method Frames (Type=1):**
- Connection.Start, Connection.Start-OK (connection negotiation)
- Connection.Tune, Connection.Tune-OK (connection tuning)
- Connection.Open, Connection.Open-OK (vhost authentication)
- Channel.Open, Channel.Open-OK
- Basic.Publish, Basic.Deliver (message publishing/delivery)
- Basic.Consume, Basic.Consume-OK (subscription)
- Many others...

**Header Frames (Type=2):**
- Message properties (content-type, delivery-mode, expiration, etc.)

**Body Frames (Type=3):**
- Message body content (can be split across multiple frames)

**Heartbeat Frames (Type=8):**
- Connection keepalive

### Connection Flow

1. **Client → Proxy:** Protocol header ("AMQP\x00\x00\x09\x01")
2. **Proxy → Client:** Start, Start-OK (server capabilities)
3. **Client → Proxy:** Secure (if using TLS, negotiated outside AMQP)
4. **Client → Proxy:** Tune (max channels, frame max)
5. **Proxy → Client:** Tune-OK
6. **Client → Proxy:** Open (username, password, vhost)
7. **Proxy → Client:** Open-OK
8. **Now channel operations can flow**

---

## Component Design

### 1. Frame Parser (New: proxy/frame.go)

**Purpose:** Parse and serialize AMQP frames

**Structures:**
```go
type FrameType uint8
const (
    FrameTypeMethod   FrameType = 1
    FrameTypeHeader  FrameType = 2
    FrameTypeBody    FrameType = 3
    FrameTypeHeartbeat FrameType = 8
)

type Frame struct {
    Type     FrameType
    Channel  uint16
    Payload  []byte
}

type MethodHeader struct {
    ClassID  uint16
    MethodID uint16
}
```

**Functions:**
- `ParseFrame(conn net.Conn) (*Frame, error)` - read and parse complete frame
- `WriteFrame(conn net.Conn, frame *Frame) error` - serialize and write frame
- `ParseMethodHeader(data []byte) (*MethodHeader, error)` - decode class/method IDs
- `SerializeMethodHeader(h *MethodHeader) []byte` - encode method header

**Error Handling:**
- Malformed frames → close connection, log error
- Incomplete frames → timeout and close
- Unknown frame types → close connection

### 2. Connection Handler (New: proxy/connection.go)

**Purpose:** Handle per-client connection lifecycle

**Structures:**
```go
type ClientConnection struct {
    Conn              net.Conn
    TLSConn           *tls.Conn
    UpstreamConn      *amqp.Connection
    ClientChannels     map[uint16]*ClientChannel
    UpstreamChannels   map[uint16]*UpstreamChannel
    ChannelMapping     map[uint16]uint16  // client → upstream channel
    Mu                sync.RWMutex
    Proxy             *Proxy
}

type ClientChannel struct {
    ID              uint16
    UpstreamID      uint16
    Safe            bool
    Operations      map[string]bool
    Mu              sync.RWMutex
    Client          *ClientConnection
}
```

**Functions:**
- `NewClientConnection(conn net.Conn, proxy *Proxy) *ClientConnection`
- `Handle()` - main loop: read frames, parse, proxy to upstream
- `ConnectToUpstream(creds *Credentials) error` - establish upstream connection
- `MapChannel(clientID, upstreamID uint16)`
- `UnmapChannel(clientID uint16)`
- `RecordChannelOperation(channelID, operation string, isSafe bool)`
- `Close()` - cleanup, handle channel safety
- `CloseUnsafeChannels()` - close channels with subscriptions, keep safe ones

**Logic:**
1. Read protocol header, respond with Start/Start-OK
2. Read Connection.Open, extract credentials
3. Connect to upstream, respond Open-OK
4. For each Channel.Open:
   - Map to upstream channel
   - Track as "safe" (we don't know yet)
5. For each method call, track if safe/unsafe
6. On client disconnect:
   - Mark all channels with Consume/Queue.Bind as "unsafe"
   - Close unsafe upstream channels
   - Keep safe channels in pool for reuse

### 3. Upstream Pool Manager (Enhance: pool/pool.go)

**Purpose:** Manage pool of upstream connections per credential set

**Add to ConnectionPool:**
```go
type ConnectionPool struct {
    // ... existing fields ...
    LastUsed         time.Time
    Mu               sync.RWMutex
}

func (p *ConnectionPool) AcquireConnection() (*PooledConnection, error)
func (p *ConnectionPool) ReleaseConnection(conn *PooledConnection)
func (p *ConnectionPool) StartIdleCleanup()
func (p *ConnectionPool) AddSafeChannel(clientID, upstreamID uint16)
func (p *ConnectionPool) GetSafeChannels() []uint16
```

**Logic:**
- **AcquireConnection:**
  - Check for existing safe connections
  - If none, establish new upstream connection
  - Mark as in-use
  - Return connection
- **ReleaseConnection:**
  - Mark as available
  - Update LastUsed timestamp
  - If idle > timeout, close connection
- **AddSafeChannel:**
  - Track safe channel numbers for reuse
  - Only channels with Basic.Publish/Basic.Get are added
- **GetSafeChannels:**
  - Return list of safe channel IDs ready for reuse

### 4. TLS Support (Enhance: proxy/listener.go)

**New: Separate listener file for AMQP vs TLS**

**Structures:**
```go
type AMQPListener struct {
    ListenAddr string
    Config     *config.Config
    TLSConfig  *tls.Config
    Proxy      *Proxy
}

func (l *AMQPListener) StartPlain() error
func (l *AMQPListener) StartTLS() error
```

**Logic:**
- `StartPlain`: `net.Listen("tcp", addr)` for non-TLS
- `StartTLS`: `net.Listen("tcp", addr)` + `tls.Server(conn, tlsConfig)` for TLS
- Determine which to use based on config or environment

### 5. Frame Proxying Logic (New: proxy/frame_proxy.go)

**Purpose:** Bidirectional frame forwarding with channel remapping

**Structures:**
```go
type FrameProxy struct {
    ClientConn      *ClientConnection
    Buffer          bytes.Buffer
    ExpectingHeader bool
}

func (fp *FrameProxy) ProxyClientToUpstream(frame *Frame) error
func (fp *FrameProxy) ProxyUpstreamToClient(frame *Frame) error
func (fp *FrameProxy) HandleBasicPublish(frame *Frame) error
func (fp *FrameProxy) HandleBasicDeliver(frame *Frame) error
```

**Logic:**

**Client → Upstream:**
1. Parse incoming frame
2. If Method:
   - Channel.Open → open upstream channel, map IDs, forward
   - Basic.Publish → record as "safe", remap channel, forward
   - Basic.Consume → record as "unsafe", remap channel, forward
   - Other methods → remap channel, forward
3. If Header/Body → remap channel number, forward

**Upstream → Client:**
1. Parse incoming frame
2. Remap upstream channel ID → client channel ID
3. Forward frame to client

**Channel Remapping:**
```
Client Channel 1 → Upstream Channel 5
Client Channel 2 → Upstream Channel 7

Frame from client on CH 1 → Forward to upstream CH 5
Frame from upstream CH 5 → Forward to client CH 1
```

### 6. Credential Extraction (Enhance: proxy/credentials.go)

**Purpose:** Parse Connection.Open frame to extract username/password/vhost

**Structures:**
```go
type Credentials struct {
    Username string
    Password string
    Vhost    string
}

type ConnectionOpenFrame struct {
    ClassID    uint16  // 10
    MethodID   uint16  // 40
    Vhost       string
    Reserved    string
    Username    string
    Password    string
}

func ParseConnectionOpen(data []byte) (*ConnectionOpenFrame, error)
```

**Implementation:**
- Parse AMQP encoded Connection.Open method
- Extract fields: vhost, username, password
- Return Credentials struct

---

## Testing Strategy

### Test Certificates Generation (New: tests/certs.go)

**Purpose:** Generate CA, server, client certificates for TLS testing

**Functions:**
```go
func GenerateTestCA() (*x509.Certificate, *rsa.PrivateKey, error)
func GenerateServerCert(caCert *x509.Certificate, caKey *rsa.PrivateKey) (*tls.Certificate, error)
func GenerateClientCert(caCert *x509.Certificate, caKey *rsa.PrivateKey) (*tls.Certificate, error)
func WriteCerts(caCert, serverCert, clientCert *tls.Certificate) error
```

**Files Generated:**
- `test_certs/ca.crt`, `test_certs/ca.key`
- `test_certs/server.crt`, `test_certs/server.key`
- `test_certs/client.crt`, `test_certs/client.key`

### Integration Test Scenarios (New: tests/integration_test.go)

**Test Suite:**

1. **TLS Connection Test:**
   - Client connects via TLS to proxy
   - Proxy connects via TLS to RabbitMQ
   - Verify certificates used correctly

2. **Connection Multiplexing Test:**
   - Connect 3 clients with same credentials
   - Verify only 1 upstream connection used
   - Verify channel IDs don't conflict

3. **Safe Channel Reuse Test:**
   - Client 1: Publish message, disconnect
   - Client 2: Connect, publish message
   - Verify upstream channel reused (same channel ID)
   - Verify faster connection time (no Connection.Open)

4. **Unsafe Channel Isolation Test:**
   - Client 1: Consume from queue, disconnect
   - Client 2: Connect, try to consume
   - Verify upstream channel closed (not reused)
   - Verify no cross-talk between clients

5. **Channel Remapping Test:**
   - Client A uses channel 1
   - Client B uses channel 1 (different connection)
   - Verify mapped to different upstream channels
   - Publish from both, verify routing

6. **Idle Timeout Test:**
   - Connect client, publish message
   - Disconnect client
   - Wait > idle timeout
   - Verify upstream connection closed
   - Verify subsequent connection creates new upstream

7. **End-to-End Message Test:**
   - Client 1: Publish message to exchange
   - Client 2: Consume from queue
   - Verify message delivered through proxy
   - Verify all frames proxied correctly

8. **Concurrent Operations Test:**
   - Multiple clients publishing concurrently
   - Multiple clients consuming concurrently
   - Verify no race conditions
   - Verify all messages delivered

---

## Error Handling Strategy

### Frame Parsing Errors
- Invalid protocol header → Close connection, log error
- Malformed frame → Close connection, log error with frame type/size
- Unsupported AMQP version → Respond with proper error frame

### Connection Errors
- TLS handshake failure → Log error, close connection
- Upstream connection failure → Respond Connection.Close to client
- Authentication failure → Return proper AMQP error

### Pool Errors
- Pool exhausted → Create new upstream connection
- Connection lost → Reconnect, attempt to remap channels
- Idle timeout → Close connection gracefully

### Channel Errors
- Channel not found → Send channel-close error
- Remapping conflict → Log error, close conflicting channel

---

## Performance Considerations

### Frame Buffering
- Use `bufio.Reader/Writer` for efficient I/O
- Batch small frames when possible

### Connection Reuse
- Cache upstream connections per credential set
- Use channel reuse for safe operations

### Goroutine Management
- One goroutine per client connection
- Avoid creating goroutines per frame
- Use channel-based signaling where appropriate

### Memory Management
- Limit frame buffer size
- Reuse byte buffers where possible
- Clean up on connection close

---

## Security Considerations

### Credential Exposure
- Never log passwords (use "***" in logs)
- Clear password from memory after authentication

### TLS Configuration
- Verify server certificates by default
- Support custom CA bundles
- Support client certificates for mTLS

### Input Validation
- Validate AMQP protocol version
- Validate frame sizes (prevent allocation attacks)
- Validate channel numbers (within range 0-65535)

---

## Deployment Configuration

### TLS Configuration Options

**Environment Variables:**
```
AMQP_TLS_ENABLED=true                    # Enable TLS listener
AMQP_TLS_CERT=/path/to/cert.pem      # Server certificate
AMQP_TLS_KEY=/path/to/key.pem         # Server private key
AMQP_TLS_CA=/path/to/ca.pem            # CA for client verification (optional)

AMQP_UPSTREAM_TLS_ENABLED=true         # Enable upstream TLS
AMQP_UPSTREAM_CA=/path/to/upstream-ca.pem
AMQP_UPSTREAM_CLIENT_CERT=/path/to/client.crt
AMQP_UPSTREAM_CLIENT_KEY=/path/to/client.key
```

### Configuration File
```toml
[tls]
enabled = true
cert = "/path/to/cert.pem"
key = "/path/to/key.pem"
ca = "/path/to/ca.pem"

[upstream_tls]
enabled = true
ca = "/path/to/upstream-ca.pem"
client_cert = "/path/to/client.crt"
client_key = "/path/to/client.key"
```

---

## Next Steps

1. Generate test certificates
2. Create TLS-enabled docker-compose.yml
3. Implement frame parser (proxy/frame.go)
4. Implement connection handler (proxy/connection.go)
5. Implement frame proxying logic (proxy/frame_proxy.go)
6. Add TLS listener support (proxy/listener.go)
7. Implement credential extraction (proxy/credentials.go)
8. Enhance connection pool with safe channel management
9. Write comprehensive integration tests
10. Verify all scenarios pass

## Verification Checklist

- [ ] TLS handshake works both client→proxy and proxy→upstream
- [ ] Multiple clients multiplex onto single upstream connection
- [ ] Safe channels (publish) are pooled and reused
- [ ] Unsafe channels (consume) are closed, not pooled
- [ ] Channel IDs are correctly remapped bidirectionally
- [ ] Messages flow end-to-end through TLS
- [ ] Idle connections are cleaned up after timeout
- [ ] No race conditions with concurrent clients
- [ ] All integration tests pass
