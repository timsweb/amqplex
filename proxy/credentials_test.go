package proxy

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

// TestParseConnectionOpenDeprecated verifies that the incorrect implementation
// is properly deprecated and returns an error directing to the correct method.
func TestParseConnectionOpenDeprecated(t *testing.T) {
	data := serializeConnectionOpen("test-vhost")

	creds, err := ParseConnectionOpen(data)
	assert.Error(t, err)
	assert.Nil(t, creds)
	assert.Contains(t, err.Error(), "deprecated")
	assert.Contains(t, err.Error(), "ParseConnectionStartOk")
}
