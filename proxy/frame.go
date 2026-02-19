package proxy

import (
	"encoding/binary"
	"errors"
	"io"
)

type FrameType uint8

const (
	FrameTypeMethod    FrameType = 1
	FrameTypeHeader    FrameType = 2
	FrameTypeBody      FrameType = 3
	FrameTypeHeartbeat FrameType = 8
)

type Frame struct {
	Type    FrameType
	Channel uint16
	Payload []byte
}

type MethodHeader struct {
	ClassID  uint16
	MethodID uint16
}

const ProtocolHeader = "AMQP\x00\x00\x09\x01"

func ParseFrame(r io.Reader) (*Frame, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	frameType := FrameType(header[0])
	channel := binary.BigEndian.Uint16(header[1:3])
	size := binary.BigEndian.Uint32(header[3:7])

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	return &Frame{
		Type:    frameType,
		Channel: channel,
		Payload: payload,
	}, nil
}

func WriteFrame(w io.Writer, frame *Frame) error {
	header := make([]byte, 7)
	header[0] = byte(frame.Type)
	binary.BigEndian.PutUint16(header[1:3], frame.Channel)
	binary.BigEndian.PutUint32(header[3:7], uint32(len(frame.Payload)))

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(frame.Payload); err != nil {
		return err
	}
	return nil
}

func ParseMethodHeader(data []byte) (*MethodHeader, error) {
	if len(data) < 4 {
		return nil, errors.New("method header too short")
	}

	return &MethodHeader{
		ClassID:  binary.BigEndian.Uint16(data[0:2]),
		MethodID: binary.BigEndian.Uint16(data[2:4]),
	}, nil
}

func SerializeMethodHeader(h *MethodHeader) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:2], h.ClassID)
	binary.BigEndian.PutUint16(buf[2:4], h.MethodID)
	return buf
}
