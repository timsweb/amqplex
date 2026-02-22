package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

type ClientConnection struct {
	Conn           net.Conn
	ClientChannels map[uint16]*ClientChannel
	ChannelMapping map[uint16]uint16
	ReverseMapping map[uint16]uint16 // For O(1) upstream → client lookup
	Mu             sync.RWMutex
	writerMu       sync.Mutex
	Proxy          *Proxy
	Reader         *bufio.Reader
	Writer         *bufio.Writer
	// Credentials extracted during handshake
	Username string
	Password string
	Vhost    string
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
		ClientChannels: make(map[uint16]*ClientChannel),
		ChannelMapping: make(map[uint16]uint16),
		ReverseMapping: make(map[uint16]uint16), // Initialize reverse map
		Proxy:          proxy,
	}
}

func (cc *ClientConnection) MapChannel(clientID, upstreamID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	cc.ChannelMapping[clientID] = upstreamID
	cc.ReverseMapping[upstreamID] = clientID // Update reverse map for O(1) lookup

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
	upstreamID, ok := cc.ChannelMapping[clientID]
	if ok {
		delete(cc.ChannelMapping, clientID)
		delete(cc.ReverseMapping, upstreamID)
		delete(cc.ClientChannels, clientID)
	}
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

// DeliverFrame writes a frame to the client connection. Thread-safe; may be
// called concurrently by ManagedUpstream's read loop.
//
// Safety: Handle() completes the client-side AMQP handshake and writes
// Connection.OpenOK before handleConnection calls ManagedUpstream.Register,
// so Handle() is never concurrent with DeliverFrame calls.
func (cc *ClientConnection) DeliverFrame(frame *Frame) error {
	cc.writerMu.Lock()
	defer cc.writerMu.Unlock()
	if err := WriteFrame(cc.Writer, frame); err != nil {
		return err
	}
	return cc.Writer.Flush()
}

// Abort forcibly closes the underlying network connection, causing any
// in-progress reads or writes to return immediately with an error.
func (cc *ClientConnection) Abort() {
	if cc.Conn != nil {
		cc.Conn.Close()
	}
}

func (cc *ClientConnection) Handle() error {
	cc.Reader = bufio.NewReader(cc.Conn)
	cc.Writer = bufio.NewWriter(cc.Conn)

	if err := cc.sendConnectionStart(); err != nil {
		return err
	}

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

	if err := cc.sendConnectionTune(); err != nil {
		return err
	}

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

	frame, err = ParseFrame(cc.Reader)
	if err != nil {
		return err
	}

	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return err
	}

	if header.ClassID != 10 || header.MethodID != 40 {
		return fmt.Errorf("expected Connection.Open (class=10, method=40), got class=%d method=%d", header.ClassID, header.MethodID)
	}

	vhost, err := ParseConnectionOpen(frame.Payload)
	if err != nil {
		return fmt.Errorf("failed to parse Connection.Open: %w", err)
	}

	cc.Mu.Lock()
	cc.Username = creds.Username
	cc.Password = creds.Password
	cc.Vhost = vhost
	cc.Mu.Unlock()

	if err := WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionOpenOK(),
	}); err != nil {
		return err
	}
	return cc.Writer.Flush()
}

func (cc *ClientConnection) sendConnectionStart() error {
	payload := serializeConnectionStart()
	if err := WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: payload,
	}); err != nil {
		return err
	}
	return cc.Writer.Flush()
}

func serializeConnectionTunePayload() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 30})
	body := make([]byte, 8)                       // channel-max(2) + frame-max(4) + heartbeat(2)
	binary.BigEndian.PutUint16(body[0:2], 0)      // channel-max = 0 (no limit)
	binary.BigEndian.PutUint32(body[2:6], 131072) // frame-max = 128KB
	binary.BigEndian.PutUint16(body[6:8], 60)     // heartbeat = 60s
	return append(header, body...)
}

func (cc *ClientConnection) sendConnectionTune() error {
	if err := WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionTunePayload(),
	}); err != nil {
		return err
	}
	return cc.Writer.Flush()
}

func serializeConnectionStart() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 10})

	payload := make([]byte, 0, 32)
	payload = append(payload, 0)                                       // version-major = 0
	payload = append(payload, 9)                                       // version-minor = 9
	payload = append(payload, serializeEmptyTable()...)                // server-properties (empty table)
	payload = append(payload, serializeLongString([]byte("PLAIN"))...) // mechanisms
	payload = append(payload, serializeLongString([]byte("en_US"))...) // locales

	return append(header, payload...)
}

func serializeConnectionOpenOK() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 41})
	return append(header, 0)
}

// isChannelOpen returns true for Channel.Open frames (class=20, method=10).
func isChannelOpen(frame *Frame) bool {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return false
	}
	return frame.Payload[0] == 0 && frame.Payload[1] == 20 &&
		frame.Payload[2] == 0 && frame.Payload[3] == 10
}

// isChannelClose returns true for Channel.Close (40) and Channel.CloseOk (41).
func isChannelClose(frame *Frame) bool {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return false
	}
	if frame.Payload[0] != 0 || frame.Payload[1] != 20 {
		return false
	}
	return frame.Payload[3] == 40 || frame.Payload[3] == 41
}

// serializeConnectionClose builds a Connection.Close method payload.
func serializeConnectionClose(replyCode uint16, replyText string) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 50})
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, replyCode)
	body = append(body, serializeShortString(replyText)...)
	body = append(body, 0, 0, 0, 0) // class-id=0, method-id=0
	return append(header, body...)
}

var unsafeMethods = map[[4]byte]bool{
	{0, 60, 0, 20}:  true, // Basic.Consume
	{0, 60, 0, 10}:  true, // Basic.Qos
	{0, 60, 0, 80}:  true, // Basic.Ack
	{0, 60, 0, 90}:  true, // Basic.Reject
	{0, 60, 0, 120}: true, // Basic.Nack
}

// markChannelSafety marks a channel unsafe if the frame is an unsafe method.
func markChannelSafety(frame *Frame, cc *ClientConnection) {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return
	}
	key := [4]byte{frame.Payload[0], frame.Payload[1], frame.Payload[2], frame.Payload[3]}
	if unsafeMethods[key] {
		cc.Mu.RLock()
		ch, ok := cc.ClientChannels[frame.Channel]
		cc.Mu.RUnlock()
		if ok {
			ch.Mu.Lock()
			ch.Safe = false
			ch.Mu.Unlock()
		}
	}
}
