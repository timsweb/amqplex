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

func ParseConnectionOpen(data []byte) (*Credentials, error) {
	header, err := ParseMethodHeader(data)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 40 {
		return nil, errors.New("not a Connection.Open frame")
	}

	offset := 4

	vhost, vhostLen, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += vhostLen

	_, reservedLen, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += reservedLen

	username, usernameLen, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}
	offset += usernameLen

	password, _, err := parseShortString(data[offset:])
	if err != nil {
		return nil, err
	}

	return &Credentials{
		Vhost:    vhost,
		Username: username,
		Password: password,
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

func serializeConnectionOpen(vhost, username, password string) []byte {
	vhostBytes := serializeShortString(vhost)
	reservedBytes := serializeShortString("")
	usernameBytes := serializeShortString(username)
	passwordBytes := serializeShortString(password)

	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})

	payload := make([]byte, 0)
	payload = append(payload, vhostBytes...)
	payload = append(payload, reservedBytes...)
	payload = append(payload, usernameBytes...)
	payload = append(payload, passwordBytes...)

	return append(header, payload...)
}

func serializeShortString(s string) []byte {
	return append([]byte{byte(len(s))}, s...)
}
