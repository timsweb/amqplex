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

	conn := &mockConnection{}
	pool.AddConnection(conn)

	pc := pool.GetConnectionByIndex(0)

	pc.AddSafeChannel(5)
	assert.True(t, pc.IsSafeChannel(5))

	pc.RemoveSafeChannel(5)
	assert.False(t, pc.IsSafeChannel(5))
}

func TestSafeChannelsPerConnection(t *testing.T) {
	p := NewConnectionPool("user", "pass", "/", 5, 65535)

	conn1 := &mockConnection{}
	conn2 := &mockConnection{}
	p.AddConnection(conn1)
	p.AddConnection(conn2)

	pc1 := p.GetConnectionByIndex(0)
	pc2 := p.GetConnectionByIndex(1)

	// Mark channel 5 as safe on connection 1 only
	pc1.AddSafeChannel(5)

	assert.True(t, pc1.IsSafeChannel(5))
	assert.False(t, pc2.IsSafeChannel(5)) // Must NOT bleed across connections
}

func TestPoolClose(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	conn1 := &mockConnection{}
	conn2 := &mockConnection{}
	pool.AddConnection(conn1)
	pool.AddConnection(conn2)

	pc1 := pool.GetConnectionByIndex(0)
	pc2 := pool.GetConnectionByIndex(1)

	pc1.AddSafeChannel(1)
	pc2.AddSafeChannel(2)

	assert.Equal(t, 2, len(pool.Connections))
	assert.True(t, pc1.IsSafeChannel(1))
	assert.True(t, pc2.IsSafeChannel(2))

	pool.Close()

	assert.Nil(t, pool.Connections)
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
