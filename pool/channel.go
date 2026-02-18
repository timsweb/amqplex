package pool

import (
	"sync"
)

type Channel struct {
	ID         int
	Operations map[string]bool
	Unsafe     bool
	mu         sync.RWMutex
}

func NewChannel(id int) *Channel {
	return &Channel{
		ID:         id,
		Operations: make(map[string]bool),
		Unsafe:     false,
	}
}

func (c *Channel) RecordOperation(op string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Operations[op] = true

	// Mark unsafe if any unsafe operation was performed
	unsafeOps := map[string]bool{
		"Basic.Consume":   true,
		"Basic.Qos":       true,
		"Basic.Ack":       true,
		"Basic.Reject":    true,
		"Basic.Nack":      true,
		"Queue.Bind":      true,
		"Queue.Unbind":    true,
		"Exchange.Bind":   true,
		"Exchange.Unbind": true,
	}

	if unsafeOps[op] {
		c.Unsafe = true
	}
}

func (c *Channel) IsSafe() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.Unsafe
}

func (c *Channel) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Operations = make(map[string]bool)
	c.Unsafe = false
}
