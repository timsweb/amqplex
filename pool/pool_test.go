package pool

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestConnectionPool(t *testing.T) {
	pool := NewConnectionPool("test-user", "test-pass", "/vhost", 5, 65535)

	// Create mock connection
	conn := &mockConnection{}
	pool.AddConnection(conn)

	// Get connection
	retrieved := pool.GetConnection()
	assert.NotNil(t, retrieved)
	assert.Equal(t, conn, retrieved.Connection)
}

type mockConnection struct{}

func (m *mockConnection) IsOpen() bool {
	return true
}

func (m *mockConnection) Close() error {
	return nil
}

func (m *mockConnection) Channel() (Channel, error) {
	return *NewChannel(1), nil
}
