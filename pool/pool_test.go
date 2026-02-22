package pool

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestConnectionReuseFromPool(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	// Add multiple connections
	conn1 := &mockConnection{}
	conn2 := &mockConnection{}
	pool.AddConnection(conn1)
	pool.AddConnection(conn2)

	// Get connection returns the first one
	retrieved := pool.GetConnection()
	assert.NotNil(t, retrieved)
	assert.Equal(t, conn1, retrieved.Connection)

	// Subsequent calls return the same connection
	retrieved2 := pool.GetConnection()
	assert.Equal(t, retrieved, retrieved2)
}

func TestSafeChannelStateTransitions(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)
	conn := &mockConnection{}
	pool.AddConnection(conn)

	pc := pool.GetConnectionByIndex(0)

	// Initially, no channels are safe
	assert.False(t, pc.IsSafeChannel(1))
	assert.False(t, pc.IsSafeChannel(2))

	// Mark channel 1 as safe
	pc.AddSafeChannel(1)
	assert.True(t, pc.IsSafeChannel(1))
	assert.False(t, pc.IsSafeChannel(2))

	// Mark channel 2 as safe
	pc.AddSafeChannel(2)
	assert.True(t, pc.IsSafeChannel(1))
	assert.True(t, pc.IsSafeChannel(2))

	// Remove channel 1 from safe state
	pc.RemoveSafeChannel(1)
	assert.False(t, pc.IsSafeChannel(1))
	assert.True(t, pc.IsSafeChannel(2))

	// Remove channel 2
	pc.RemoveSafeChannel(2)
	assert.False(t, pc.IsSafeChannel(1))
	assert.False(t, pc.IsSafeChannel(2))
}

func TestConcurrentPoolAccess(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)
	var wg sync.WaitGroup

	// Concurrently add connections
	numConnections := 10
	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := &mockConnection{}
			pool.AddConnection(conn)
		}()
	}

	// Concurrently mark channels as safe
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc := pool.GetConnection()
			if pc != nil {
				pc.AddSafeChannel(uint16(i % 10))
			}
		}()
	}

	wg.Wait()

	// Verify pool has all connections
	pool.mu.RLock()
	assert.Equal(t, numConnections, len(pool.Connections))
	pool.mu.RUnlock()
}

func TestEmptyPoolGetConnection(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	// Getting connection from empty pool returns nil
	conn := pool.GetConnection()
	assert.Nil(t, conn)
}

func TestGetConnectionByIndexBounds(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)
	conn := &mockConnection{}
	pool.AddConnection(conn)

	// Valid index
	pc := pool.GetConnectionByIndex(0)
	assert.NotNil(t, pc)

	// Out of bounds indices return nil
	pc = pool.GetConnectionByIndex(-1)
	assert.Nil(t, pc)

	pc = pool.GetConnectionByIndex(1)
	assert.Nil(t, pc)

	pc = pool.GetConnectionByIndex(100)
	assert.Nil(t, pc)
}

func TestPoolCloseClosesAllConnections(t *testing.T) {
	pool := NewConnectionPool("user", "pass", "/", 5, 65535)

	// Track which connections were closed
	var closedConns []*mockConnection
	conn1 := &trackableCloseConnection{closed: &closedConns}
	conn2 := &trackableCloseConnection{closed: &closedConns}

	pool.AddConnection(conn1)
	pool.AddConnection(conn2)

	// Mark some channels as safe
	pc1 := pool.GetConnectionByIndex(0)
	pc2 := pool.GetConnectionByIndex(1)
	pc1.AddSafeChannel(1)
	pc2.AddSafeChannel(2)

	pool.Close()

	// All connections should be closed
	assert.Equal(t, 2, len(closedConns))
	assert.Nil(t, pool.Connections)
}

type trackableCloseConnection struct {
	mockConnection
	closed *[]*mockConnection
}

func (t *trackableCloseConnection) Close() error {
	*t.closed = append(*t.closed, &t.mockConnection)
	return nil
}
