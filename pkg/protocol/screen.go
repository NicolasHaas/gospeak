package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	ScreenHeaderSize = 8
	MaxScreenPacket  = 2 * 1024 * 1024
	MaxScreenFormat  = 16
)

type ScreenAuth struct {
	SessionID uint32
	Token     string
}

type ScreenPacket struct {
	SessionID uint32
	SeqNum    uint32
	Payload   []byte
}

type ScreenFrame struct {
	Timestamp int64
	Width     int32
	Height    int32
	Format    string
	Data      []byte
}

func (p *ScreenPacket) MarshalHeader() []byte {
	h := make([]byte, ScreenHeaderSize)
	binary.BigEndian.PutUint32(h[0:4], p.SessionID)
	binary.BigEndian.PutUint32(h[4:8], p.SeqNum)
	return h
}

func (p *ScreenPacket) Marshal() []byte {
	h := p.MarshalHeader()
	out := make([]byte, len(h)+len(p.Payload))
	copy(out, h)
	copy(out[len(h):], p.Payload)
	return out
}

func UnmarshalScreenPacket(data []byte) (*ScreenPacket, error) {
	if len(data) < ScreenHeaderSize {
		return nil, errors.New("protocol: screen packet too short")
	}
	pkt := &ScreenPacket{
		SessionID: binary.BigEndian.Uint32(data[0:4]),
		SeqNum:    binary.BigEndian.Uint32(data[4:8]),
		Payload:   make([]byte, len(data)-ScreenHeaderSize),
	}
	copy(pkt.Payload, data[ScreenHeaderSize:])
	return pkt, nil
}

func WriteScreenPacket(w io.Writer, pkt *ScreenPacket) error {
	data := pkt.Marshal()
	if len(data) > MaxScreenPacket {
		return fmt.Errorf("protocol: screen packet too large: %d bytes", len(data))
	}
	n, err := checkUint32(len(data))
	if err != nil {
		return fmt.Errorf("protocol: write screen length: %w", err)
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, n)
	if _, err := w.Write(lenBuf); err != nil {
		return fmt.Errorf("protocol: write screen length: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("protocol: write screen payload: %w", err)
	}
	return nil
}

func ReadScreenPacket(r io.Reader) (*ScreenPacket, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, fmt.Errorf("protocol: read screen length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > MaxScreenPacket {
		return nil, fmt.Errorf("protocol: screen packet too large: %d bytes", length)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("protocol: read screen payload: %w", err)
	}
	return UnmarshalScreenPacket(data)
}

func MarshalScreenFrame(frame *ScreenFrame) ([]byte, error) {
	if frame == nil {
		return nil, errors.New("protocol: nil screen frame")
	}
	if len(frame.Format) == 0 || len(frame.Format) > MaxScreenFormat {
		return nil, fmt.Errorf("protocol: invalid screen format length: %d", len(frame.Format))
	}
	buf := bytes.NewBuffer(make([]byte, 0, 17+len(frame.Format)+len(frame.Data)))
	if err := binary.Write(buf, binary.BigEndian, frame.Timestamp); err != nil {
		return nil, fmt.Errorf("protocol: write timestamp: %w", err)
	}
	if err := binary.Write(buf, binary.BigEndian, frame.Width); err != nil {
		return nil, fmt.Errorf("protocol: write width: %w", err)
	}
	if err := binary.Write(buf, binary.BigEndian, frame.Height); err != nil {
		return nil, fmt.Errorf("protocol: write height: %w", err)
	}
	b, err := checkByte(len(frame.Format))
	if err != nil {
		return nil, err
	}
	if err := buf.WriteByte(b); err != nil {
		return nil, fmt.Errorf("protocol: write format length: %w", err)
	}
	if _, err := buf.WriteString(frame.Format); err != nil {
		return nil, fmt.Errorf("protocol: write format: %w", err)
	}
	if _, err := buf.Write(frame.Data); err != nil {
		return nil, fmt.Errorf("protocol: write frame data: %w", err)
	}
	return buf.Bytes(), nil
}

func UnmarshalScreenFrame(data []byte) (*ScreenFrame, error) {
	if len(data) < 17 {
		return nil, errors.New("protocol: screen frame too short")
	}
	timestamp, err := checkInt64(binary.BigEndian.Uint64(data[0:8]))
	if err != nil {
		return nil, err
	}
	width, err := checkInt32(binary.BigEndian.Uint32(data[8:12]))
	if err != nil {
		return nil, err
	}
	height, err := checkInt32(binary.BigEndian.Uint32(data[12:16]))
	if err != nil {
		return nil, err
	}
	frame := &ScreenFrame{
		Timestamp: timestamp,
		Width:     width,
		Height:    height,
	}
	formatLen := int(data[16])
	if formatLen == 0 || formatLen > MaxScreenFormat || len(data) < 17+formatLen {
		return nil, errors.New("protocol: invalid screen format")
	}
	frame.Format = string(data[17 : 17+formatLen])
	frame.Data = make([]byte, len(data)-(17+formatLen))
	copy(frame.Data, data[17+formatLen:])
	return frame, nil
}

func WriteScreenAuth(w io.Writer, auth *ScreenAuth) error {
	if auth == nil || auth.SessionID == 0 || auth.Token == "" {
		return errors.New("protocol: invalid screen auth")
	}
	if len(auth.Token) > 512 {
		return fmt.Errorf("protocol: screen auth token too long: %d", len(auth.Token))
	}
	tokenLen, err := checkUint16(len(auth.Token))
	if err != nil {
		return fmt.Errorf("protocol: write screen auth: %w", err)
	}
	payload := make([]byte, 6+len(auth.Token))
	binary.BigEndian.PutUint32(payload[0:4], auth.SessionID)
	binary.BigEndian.PutUint16(payload[4:6], tokenLen)
	copy(payload[6:], auth.Token)
	lenVal, err := checkUint32(len(payload))
	if err != nil {
		return fmt.Errorf("protocol: write screen auth: %w", err)
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, lenVal)
	if _, err := w.Write(lenBuf); err != nil {
		return fmt.Errorf("protocol: write screen auth length: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("protocol: write screen auth payload: %w", err)
	}
	return nil
}

func ReadScreenAuth(r io.Reader) (*ScreenAuth, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, fmt.Errorf("protocol: read screen auth length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length < 6 || length > 1024 {
		return nil, fmt.Errorf("protocol: invalid screen auth length: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("protocol: read screen auth payload: %w", err)
	}
	tokenLen := int(binary.BigEndian.Uint16(payload[4:6]))
	if tokenLen == 0 || 6+tokenLen != len(payload) {
		return nil, errors.New("protocol: invalid screen auth token")
	}
	return &ScreenAuth{
		SessionID: binary.BigEndian.Uint32(payload[0:4]),
		Token:     string(payload[6:]),
	}, nil
}

func checkUint32(value int) (uint32, error) {
	if value < 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("protocol: value %d out of uint32 range", value)
	}
	return uint32(value), nil
}

func checkUint16(value int) (uint16, error) {
	if value < 0 || value > math.MaxUint16 {
		return 0, fmt.Errorf("protocol: value %d out of uint16 range", value)
	}
	return uint16(value), nil
}

func checkByte(value int) (byte, error) {
	if value < 0 || value > math.MaxUint8 {
		return 0, fmt.Errorf("protocol: value %d out of byte range", value)
	}
	return byte(value), nil
}

func checkInt64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("protocol: value %d out of int64 range", value)
	}
	return int64(value), nil
}

func checkInt32(value uint32) (int32, error) {
	if value > math.MaxInt32 {
		return 0, fmt.Errorf("protocol: value %d out of int32 range", value)
	}
	return int32(value), nil
}
