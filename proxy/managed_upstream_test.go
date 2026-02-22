package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestManagedUpstream(maxChannels uint16) *ManagedUpstream {
	return &ManagedUpstream{
		username:      "guest",
		password:      "guest",
		vhost:         "/",
		maxChannels:   maxChannels,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
	}
}

// stubClient implements clientWriter for tests.
type stubClient struct {
	frames []*Frame
}

func (s *stubClient) DeliverFrame(f *Frame) error {
	s.frames = append(s.frames, f)
	return nil
}
func (s *stubClient) Abort() {}

func TestAllocateChannel_AssignsLowestFreeID(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	upstreamID, err := m.AllocateChannel(1, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(1), upstreamID)

	upstreamID2, err := m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(2), upstreamID2)
}

func TestAllocateChannel_ReleasedChannelIsReused(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	id, _ := m.AllocateChannel(1, stub)
	assert.Equal(t, uint16(1), id)

	m.ReleaseChannel(id)

	id2, err := m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(1), id2) // reuses freed slot
}

func TestAllocateChannel_ErrorWhenFull(t *testing.T) {
	m := newTestManagedUpstream(2) // only 2 channels allowed
	stub := &stubClient{}

	_, err := m.AllocateChannel(1, stub)
	assert.NoError(t, err)
	_, err = m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	_, err = m.AllocateChannel(3, stub)
	assert.ErrorContains(t, err, "no free upstream channel")
}

func TestAllocateChannel_ClientChannelIDStored(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	upstreamID, _ := m.AllocateChannel(7, stub)

	m.mu.Lock()
	entry := m.channelOwners[upstreamID]
	m.mu.Unlock()

	assert.Equal(t, uint16(7), entry.clientChanID)
	assert.Equal(t, clientWriter(stub), entry.owner)
}

func TestHasCapacity(t *testing.T) {
	m := newTestManagedUpstream(2)
	stub := &stubClient{}
	assert.True(t, m.HasCapacity())
	m.AllocateChannel(1, stub)
	m.AllocateChannel(2, stub)
	assert.False(t, m.HasCapacity())
}
