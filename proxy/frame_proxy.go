package proxy

import (
	"io"
	"sync"
)

// FrameProxy remaps AMQP channel IDs and forwards frames between a client
// connection and an upstream connection.
type FrameProxy struct {
	ClientConn     *ClientConnection
	UpstreamWriter io.Writer // writes go to the upstream RabbitMQ connection
	ClientWriter   io.Writer // writes go back to the AMQP client
	mu             sync.Mutex
}

// NewFrameProxy creates a FrameProxy. upstreamWriter receives client→upstream frames;
// clientWriter receives upstream→client frames. Either may be nil in tests.
func NewFrameProxy(clientConn *ClientConnection, upstreamWriter, clientWriter io.Writer) *FrameProxy {
	return &FrameProxy{
		ClientConn:     clientConn,
		UpstreamWriter: upstreamWriter,
		ClientWriter:   clientWriter,
	}
}

// ProxyClientToUpstream remaps the frame's channel from client-space to upstream-space
// and writes it to the upstream writer. A nil UpstreamWriter is a no-op.
func (fp *FrameProxy) ProxyClientToUpstream(frame *Frame) error {
	if fp.UpstreamWriter == nil {
		return nil
	}

	fp.ClientConn.Mu.RLock()
	upstreamID, ok := fp.ClientConn.ChannelMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	remapped := *frame
	if ok {
		remapped.Channel = upstreamID
	}

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.UpstreamWriter, &remapped)
}

// ProxyUpstreamToClient remaps the frame's channel from upstream-space to client-space
// and writes it to the client writer. Channel-0 frames (heartbeats, Connection.Close,
// Connection.Blocked) are passed through unconditionally. Frames on other channels with
// no mapping are silently dropped (they belong to a different client).
func (fp *FrameProxy) ProxyUpstreamToClient(frame *Frame) error {
	if fp.ClientWriter == nil {
		return nil
	}

	// Channel 0 is the AMQP connection-level channel. It is never in ReverseMapping
	// but must always be forwarded to the client.
	if frame.Channel == 0 {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		return WriteFrame(fp.ClientWriter, frame)
	}

	fp.ClientConn.Mu.RLock()
	clientID, found := fp.ClientConn.ReverseMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	if !found {
		return nil
	}

	remapped := *frame
	remapped.Channel = clientID

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.ClientWriter, &remapped)
}
