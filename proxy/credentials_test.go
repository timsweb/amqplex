package proxy

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseConnectionStartOk(t *testing.T) {
	// PLAIN mechanism format: \0username\0password
	response := serializeConnectionStartOkResponse("user", "pass")
	data := serializeConnectionStartOk("PLAIN", response)

	creds, err := ParseConnectionStartOk(data)
	assert.NoError(t, err)
	assert.Equal(t, "user", creds.Username)
	assert.Equal(t, "pass", creds.Password)
}
