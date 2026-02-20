package proxy

import (
	"errors"
)

type Credentials struct {
	Username string
	Password string
	Vhost    string
}

type ConnectionOpenFrame struct {
	Vhost    string
	Reserved string
	Username string
	Password string
}

// ParseConnectionOpen is DEPRECATED - do not use for credential extraction.
// Per AMQP 0-9-1 spec, credentials are extracted from Connection.StartOk (method 20),
// not Connection.Open (method 40). Connection.Open only specifies which vhost to use
// after authentication has already occurred.
// Use ParseConnectionStartOk instead for credential extraction.
func ParseConnectionOpen(data []byte) (*Credentials, error) {
	return nil, errors.New("ParseConnectionOpen is deprecated. Use ParseConnectionStartOk for credential extraction (credentials are in Connection.StartOk, not Connection.Open)")
}

func parseShortString(data []byte) (string, int, error) {
	if len(data) < 1 {
		return "", 0, errors.New("data too short")
	}
	length := int(data[0])
	if len(data) < 1+length {
		return "", 0, errors.New("invalid string length")
	}
	return string(data[1 : 1+length]), 1 + length, nil
}

// serializeConnectionOpen is DEPRECATED - not used for credential extraction.
// Kept for potential future use (parsing Connection.Open to get vhost).
func serializeConnectionOpen(vhost string) []byte {
	vhostBytes := serializeShortString(vhost)
	reservedBytes := serializeShortString("")

	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})

	payload := make([]byte, 0)
	payload = append(payload, vhostBytes...)
	payload = append(payload, reservedBytes...)

	return append(header, payload...)
}

func serializeShortString(s string) []byte {
	return append([]byte{byte(len(s))}, s...)
}
