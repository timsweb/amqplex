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

func TestSafeChannelManagement(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	pool.AddSafeChannel(5)
	assert.True(t, pool.IsSafeChannel(5))

	pool.RemoveSafeChannel(5)
	assert.False(t, pool.IsSafeChannel(5))
}

func TestPoolClose(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	conn1 := &mockConnection{}
	conn2 := &mockConnection{}
	pool.AddConnection(conn1)
	pool.AddConnection(conn2)

	pool.AddSafeChannel(1)
	pool.AddSafeChannel(2)

	assert.Equal(t, 2, len(pool.Connections))
	assert.True(t, pool.IsSafeChannel(1))
	assert.True(t, pool.IsSafeChannel(2))

	pool.Close()

	assert.Nil(t, pool.Connections)
	assert.False(t, pool.IsSafeChannel(1))
	assert.False(t, pool.IsSafeChannel(2))
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
