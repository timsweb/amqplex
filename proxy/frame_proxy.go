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
		Writer:     &bytes.Buffer{},
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

	return WriteFrame(fp.Writer, &remapped)
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

	return WriteFrame(fp.Writer, &remapped)
}
