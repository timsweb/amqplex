package proxy

import (
	"bytes"
	"sync"
)

type FrameProxy struct {
	ClientConn *ClientConnection
	Writer     *bytes.Buffer
	mu         sync.Mutex // Protect Writer from concurrent access
}

func NewFrameProxy(clientConn *ClientConnection) *FrameProxy {
	return &FrameProxy{
		ClientConn: clientConn,
		Writer:     &bytes.Buffer{},
	}
}

func (fp *FrameProxy) Reset() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.Writer.Reset()
}

func (fp *FrameProxy) ProxyClientToUpstream(frame *Frame) error {
	fp.ClientConn.Mu.RLock()
	upstreamID, ok := fp.ClientConn.ChannelMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	if !ok {
		// No upstream channel mapped yet, forward as-is
		return WriteFrame(fp.Writer, frame)
	}

	remapped := *frame
	remapped.Channel = upstreamID

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.Writer, &remapped)
}

func (fp *FrameProxy) ProxyUpstreamToClient(frame *Frame) error {
	// Use reverse mapping for O(1) lookup
	fp.ClientConn.Mu.RLock()
	clientID, found := fp.ClientConn.ReverseMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	if !found {
		// No mapping exists - this frame should be ignored
		// Can happen for frames on unmapped upstream channels
		return nil
	}

	remapped := *frame
	remapped.Channel = clientID

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.Writer, &remapped)
}
