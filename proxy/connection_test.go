package proxy

import (
	"github.com/stretchr/testify/assert"
	"net"
	"testing"
)

func TestNewClientConnection(t *testing.T) {
	conn := &net.TCPConn{}
	proxy := &Proxy{}

	clientConn := NewClientConnection(conn, proxy)
	assert.NotNil(t, clientConn)
	assert.Equal(t, conn, clientConn.Conn)
}
