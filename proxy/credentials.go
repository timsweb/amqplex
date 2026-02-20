package proxy

import (
	"errors"
)

type Credentials struct {
	Username string
	Password string
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

func serializeShortString(s string) []byte {
	return append([]byte{byte(len(s))}, s...)
}
