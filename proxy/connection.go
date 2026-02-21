package proxy

import (
	"net"
	"sync"
)

type ClientConnection struct {
	Conn           net.Conn
	UpstreamConn   interface{} // Will be *amqp.Connection when established
	ClientChannels map[uint16]*ClientChannel
	ChannelMapping map[uint16]uint16
	ReverseMapping map[uint16]uint16 // For O(1) upstream → client lookup
	Mu             sync.RWMutex
	Proxy          *Proxy
}

type ClientChannel struct {
	ID         uint16
	UpstreamID uint16
	Safe       bool
	Operations map[string]bool
	Mu         sync.RWMutex
	Client     *ClientConnection
}

func NewClientConnection(conn net.Conn, proxy *Proxy) *ClientConnection {
	return &ClientConnection{
		Conn:           conn,
		ClientChannels: make(map[uint16]*ClientChannel),
		ChannelMapping: make(map[uint16]uint16),
		ReverseMapping: make(map[uint16]uint16), // Initialize reverse map
		Proxy:          proxy,
	}
}

func (cc *ClientConnection) MapChannel(clientID, upstreamID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	cc.ChannelMapping[clientID] = upstreamID
	cc.ReverseMapping[upstreamID] = clientID // Update reverse map for O(1) lookup

	if channel, ok := cc.ClientChannels[clientID]; ok {
		channel.UpstreamID = upstreamID
	} else {
		cc.ClientChannels[clientID] = &ClientChannel{
			ID:         clientID,
			UpstreamID: upstreamID,
			Safe:       true,
			Operations: make(map[string]bool),
			Client:     cc,
		}
	}
}

func (cc *ClientConnection) UnmapChannel(clientID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	upstreamID, ok := cc.ChannelMapping[clientID]
	if ok {
		delete(cc.ChannelMapping, clientID)
		delete(cc.ReverseMapping, upstreamID) // Remove from reverse map too
	}
}

func (cc *ClientConnection) RecordChannelOperation(channelID uint16, operation string, isSafe bool) {
	cc.Mu.RLock()
	channel, ok := cc.ClientChannels[channelID]
	cc.Mu.RUnlock()

	if !ok {
		return
	}

	channel.Mu.Lock()
	defer channel.Mu.Unlock()
	channel.Operations[operation] = true
	if !isSafe {
		channel.Safe = false
	}
}
