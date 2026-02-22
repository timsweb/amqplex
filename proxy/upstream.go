package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
)

// UpstreamConn represents an established AMQP connection to upstream RabbitMQ.
type UpstreamConn struct {
	Conn   net.Conn
	Reader *bufio.Reader
	Writer *bufio.Writer
}

// performUpstreamHandshake performs the AMQP client handshake against an already-dialed
// net.Conn, using the given credentials and vhost. Returns a ready-to-use UpstreamConn.
func performUpstreamHandshake(conn net.Conn, username, password, vhost string) (*UpstreamConn, error) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	// Send protocol header
	if _, err := io.WriteString(w, ProtocolHeader); err != nil {
		return nil, fmt.Errorf("failed to send protocol header: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.Start
	frame, err := ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.Start: %w", err)
	}
	header, err := ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 10 {
		return nil, fmt.Errorf("expected Connection.Start (10,10), got (%d,%d)", header.ClassID, header.MethodID)
	}

	// Send Connection.StartOk with PLAIN credentials
	response := serializeConnectionStartOkResponse(username, password)
	startOk := serializeConnectionStartOk("PLAIN", response)
	if err := WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: startOk}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.StartOk: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.Tune
	frame, err = ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.Tune: %w", err)
	}
	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 30 {
		return nil, fmt.Errorf("expected Connection.Tune (10,30), got (%d,%d)", header.ClassID, header.MethodID)
	}

	// Send Connection.TuneOk — echo back the same parameters
	if err := WriteFrame(w, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionTuneOk(frame.Payload),
	}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.TuneOk: %w", err)
	}

	// Send Connection.Open with vhost
	openPayload := serializeConnectionOpen(vhost)
	if err := WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: openPayload}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.Open: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.OpenOK
	frame, err = ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.OpenOK: %w", err)
	}
	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 41 {
		return nil, fmt.Errorf("expected Connection.OpenOK (10,41), got (%d,%d)", header.ClassID, header.MethodID)
	}

	return &UpstreamConn{Conn: conn, Reader: r, Writer: w}, nil
}

// serializeConnectionTuneOk builds a Connection.TuneOk payload that echoes
// the parameters from the upstream's Connection.Tune frame.
func serializeConnectionTuneOk(tunePayload []byte) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 31})
	// Tune body is the same wire format as TuneOk: channel-max, frame-max, heartbeat
	// Just echo the 8 bytes of body from the upstream tune.
	if len(tunePayload) >= 12 {
		return append(header, tunePayload[4:12]...)
	}
	// Fallback: send reasonable defaults
	body := serializeConnectionTunePayload()
	return append(header, body[4:]...)
}

// serializeConnectionOpen builds a Connection.Open payload for the given vhost.
func serializeConnectionOpen(vhost string) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	payload := serializeShortString(vhost)
	payload = append(payload, serializeShortString("")...) // reserved1
	payload = append(payload, 0)                           // reserved2 (bit)
	return append(header, payload...)
}
