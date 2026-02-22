package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

type Credentials struct {
	Username string
	Password string
}

func ParseConnectionStartOk(data []byte) (*Credentials, error) {
	header, err := ParseMethodHeader(data)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 11 {
		return nil, errors.New("not a Connection.StartOk frame")
	}

	offset := 4

	// Skip client-properties (table)
	_, tableEnd, err := parseTable(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += tableEnd

	// Skip mechanism (shortstr) - e.g., "PLAIN"
	_, mechLen, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += mechLen

	// Parse response (longstr) - contains \0username\0password
	response, _, err := parseLongString(data[offset:])
	if err != nil {
		return nil, err
	}

	// Parse PLAIN format: \0auth-id\0username\0password
	parts := bytes.Split(response, []byte{0})
	if len(parts) != 3 {
		return nil, errors.New("invalid PLAIN auth format")
	}

	return &Credentials{
		Username: string(parts[1]),
		Password: string(parts[2]),
	}, nil
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

func parseLongString(data []byte) ([]byte, int, error) {
	if len(data) < 4 {
		return nil, 0, errors.New("data too short for longstr")
	}
	length := int(binary.BigEndian.Uint32(data[0:4]))
	if len(data) < 4+length {
		return nil, 0, errors.New("invalid long string length")
	}
	return data[4 : 4+length], 4 + length, nil
}

func parseTable(data []byte) ([]byte, int, error) {
	// For now, skip tables - not needed for credential extraction
	if len(data) < 4 {
		return nil, 0, errors.New("table too short")
	}
	length := int(binary.BigEndian.Uint32(data[0:4]))
	if len(data) < 4+length {
		return nil, 0, errors.New("invalid table length")
	}
	return data[4 : 4+length], 4 + length, nil
}

func serializeConnectionStartOk(mechanism string, response []byte) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 11})

	payload := make([]byte, 0)
	payload = append(payload, serializeEmptyTable()...)
	payload = append(payload, serializeShortString(mechanism)...)
	payload = append(payload, serializeLongString(response)...)
	payload = append(payload, serializeShortString("en_US")...)

	return append(header, payload...)
}

func serializeConnectionStartOkResponse(username, password string) []byte {
	response := []byte{0}
	response = append(response, []byte(username)...)
	response = append(response, 0)
	response = append(response, []byte(password)...)
	return response
}

func serializeEmptyTable() []byte {
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, 0)
	return lengthBytes
}

func serializeLongString(data []byte) []byte {
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, uint32(len(data)))
	result := append(lengthBytes, data...)
	return result
}

func serializeShortString(s string) []byte {
	return append([]byte{byte(len(s))}, s...)
}

// ParseConnectionOpen extracts the vhost from a Connection.Open frame payload.
func ParseConnectionOpen(data []byte) (string, error) {
	header, err := ParseMethodHeader(data)
	if err != nil {
		return "", err
	}
	if header.ClassID != 10 || header.MethodID != 40 {
		return "", fmt.Errorf("expected Connection.Open (class=10, method=40), got class=%d method=%d", header.ClassID, header.MethodID)
	}
	if len(data) < 5 {
		return "", errors.New("Connection.Open payload too short")
	}
	vhost, _, err := parseShortString(data[4:])
	if err != nil {
		return "", fmt.Errorf("failed to parse vhost: %w", err)
	}
	return vhost, nil
}
