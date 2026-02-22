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

func TestParseConnectionOpen(t *testing.T) {
	// Build a Connection.Open payload: method header + shortstr vhost
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	vhostBytes := serializeShortString("/staging")
	payload := append(header, vhostBytes...)

	vhost, err := ParseConnectionOpen(payload)
	assert.NoError(t, err)
	assert.Equal(t, "/staging", vhost)
}

func TestParseConnectionOpenDefaultVhost(t *testing.T) {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	vhostBytes := serializeShortString("/")
	payload := append(header, vhostBytes...)

	vhost, err := ParseConnectionOpen(payload)
	assert.NoError(t, err)
	assert.Equal(t, "/", vhost)
}

func TestParseConnectionOpenWrongMethod(t *testing.T) {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 11})
	payload := append(header, 0)

	_, err := ParseConnectionOpen(payload)
	assert.Error(t, err)
}
