package proxy

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseFrame(t *testing.T) {
	frame := &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: []byte{10, 40},
	}

	buf := bytes.Buffer{}
	WriteFrame(&buf, frame)

	parsed, err := ParseFrame(&buf)
	assert.NoError(t, err)
	assert.Equal(t, FrameTypeMethod, parsed.Type)
	assert.Equal(t, uint16(0), parsed.Channel)
}
