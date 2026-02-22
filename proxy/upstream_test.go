package proxy

import (
	"bufio"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeUpstream simulates a minimal RabbitMQ server for testing the upstream handshake.
func fakeUpstream(t *testing.T) (net.Conn, func()) {
	t.Helper()
	serverConn, clientConn, err := createPipe()
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer serverConn.Close()
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)

		// Read protocol header
		header := make([]byte, 8)
		r.Read(header)

		// Send Connection.Start
		start := serializeConnectionStart()
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: start})
		w.Flush()

		// Read Connection.StartOk
		ParseFrame(r)

		// Send Connection.Tune
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: serializeConnectionTunePayload()})
		w.Flush()

		// Read Connection.TuneOk
		ParseFrame(r)

		// Read Connection.Open
		ParseFrame(r)

		// Send Connection.OpenOK
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: serializeConnectionOpenOK()})
		w.Flush()
	}()

	return clientConn, func() { clientConn.Close() }
}

func createPipe() (net.Conn, net.Conn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()

	var serverConn net.Conn
	done := make(chan error, 1)
	go func() {
		var err error
		serverConn, err = ln.Accept()
		done <- err
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	if err := <-done; err != nil {
		return nil, nil, err
	}
	return serverConn, clientConn, nil
}

func TestDialUpstream(t *testing.T) {
	serverConn, cleanup := fakeUpstream(t)
	defer cleanup()

	upstreamConn, err := performUpstreamHandshake(serverConn, "testuser", "testpass", "/testvhost")
	assert.NoError(t, err)
	assert.NotNil(t, upstreamConn)
}
