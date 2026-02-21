package proxy

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestProxyClientToUpstream(t *testing.T) {
	clientConn := &ClientConnection{}
	fp := NewFrameProxy(clientConn)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}
