package proxy

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseConnectionOpen(t *testing.T) {
	data := serializeConnectionOpen("test-vhost", "user", "pass")

	creds, err := ParseConnectionOpen(data)
	assert.NoError(t, err)
	assert.Equal(t, "test-vhost", creds.Vhost)
	assert.Equal(t, "user", creds.Username)
	assert.Equal(t, "pass", creds.Password)
}
