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

func TestParseConnectionStartOkAMQPLAIN(t *testing.T) {
	// AMQPLAIN format: inline table fields LOGIN + PASSWORD (no leading length prefix)
	response := buildAMQPLAINResponse("myuser", "mypass")
	data := serializeConnectionStartOk("AMQPLAIN", response)

	creds, err := ParseConnectionStartOk(data)
	assert.NoError(t, err)
	assert.Equal(t, "myuser", creds.Username)
	assert.Equal(t, "mypass", creds.Password)
}

func buildAMQPLAINResponse(username, password string) []byte {
	var b []byte
	b = append(b, serializeShortString("LOGIN")...)
	b = append(b, 'S')
	b = append(b, serializeLongString([]byte(username))...)
	b = append(b, serializeShortString("PASSWORD")...)
	b = append(b, 'S')
	b = append(b, serializeLongString([]byte(password))...)
	return b
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
