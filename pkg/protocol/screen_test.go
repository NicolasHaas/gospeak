package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

type shortScreenWriter struct {
	buf      bytes.Buffer
	maxWrite int
	writes   int
}

type screenLengthOnlyReader struct {
	length         [4]byte
	offset         int
	readPastLength bool
}

func (r *screenLengthOnlyReader) Read(p []byte) (int, error) {
	if r.offset < len(r.length) {
		n := copy(p, r.length[r.offset:])
		r.offset += n
		return n, nil
	}
	r.readPastLength = true
	return 0, errors.New("unexpected read past screen length")
}

func (w *shortScreenWriter) Write(p []byte) (int, error) {
	w.writes++
	if len(p) > w.maxWrite {
		p = p[:w.maxWrite]
	}
	return w.buf.Write(p)
}

func TestScreenPacketRoundTrip(t *testing.T) {
	original := &ScreenPacket{
		SessionID: 42,
		SeqNum:    7,
		Payload:   []byte("ciphertext"),
	}

	var buf bytes.Buffer
	if err := WriteScreenPacket(&buf, original); err != nil {
		t.Fatalf("WriteScreenPacket: %v", err)
	}

	roundTrip, err := ReadScreenPacket(&buf)
	if err != nil {
		t.Fatalf("ReadScreenPacket: %v", err)
	}
	if roundTrip.SessionID != original.SessionID {
		t.Fatalf("SessionID = %d, want %d", roundTrip.SessionID, original.SessionID)
	}
	if roundTrip.SeqNum != original.SeqNum {
		t.Fatalf("SeqNum = %d, want %d", roundTrip.SeqNum, original.SeqNum)
	}
	if !bytes.Equal(roundTrip.Payload, original.Payload) {
		t.Fatalf("Payload = %q, want %q", roundTrip.Payload, original.Payload)
	}
}

func TestReadScreenPacketValidatedRejectsBeforePayloadRead(t *testing.T) {
	const payloadLength uint32 = 15
	payload := []byte("encrypted-frame")
	var wire bytes.Buffer
	var prefix [4 + ScreenHeaderSize]byte
	binary.BigEndian.PutUint32(prefix[0:4], ScreenHeaderSize+payloadLength)
	binary.BigEndian.PutUint32(prefix[4:8], 42)
	binary.BigEndian.PutUint32(prefix[8:12], 7)
	wire.Write(prefix[:])
	wire.Write(payload)

	var header *ScreenPacketHeader
	_, err := ReadScreenPacketValidated(&wire, func(got *ScreenPacketHeader) ScreenPacketReadDecision {
		header = got
		return ScreenPacketReject
	})
	if err == nil {
		t.Fatal("ReadScreenPacketValidated accepted rejected packet")
	}
	if header.SessionID != 42 || header.SeqNum != 7 || header.PayloadLength() != payloadLength {
		t.Fatalf("header = %#v payload=%d, want session=42 sequence=7 payload=%d", header, header.PayloadLength(), len(payload))
	}
	if wire.Len() != len(payload) {
		t.Fatalf("bytes remaining = %d, want payload length %d", wire.Len(), len(payload))
	}
}

func TestReadScreenPacketValidatedDiscardsWithoutLosingFraming(t *testing.T) {
	pkt := &ScreenPacket{SessionID: 42, SeqNum: 7, Payload: []byte("discard")}
	var wire bytes.Buffer
	if err := WriteScreenPacket(&wire, pkt); err != nil {
		t.Fatalf("WriteScreenPacket(discard): %v", err)
	}
	if err := WriteScreenPacket(&wire, pkt); err != nil {
		t.Fatalf("WriteScreenPacket(read): %v", err)
	}
	if _, err := ReadScreenPacketValidated(&wire, func(*ScreenPacketHeader) ScreenPacketReadDecision {
		return ScreenPacketDiscard
	}); !errors.Is(err, ErrScreenPacketDiscarded) {
		t.Fatalf("discard error = %v, want %v", err, ErrScreenPacketDiscarded)
	}
	got, err := ReadScreenPacket(&wire)
	if err != nil {
		t.Fatalf("ReadScreenPacket after discard: %v", err)
	}
	if !bytes.Equal(got.Payload, pkt.Payload) {
		t.Fatalf("payload after discard = %q, want %q", got.Payload, pkt.Payload)
	}
}

func TestReadScreenPacketRejectsShortPacket(t *testing.T) {
	var wire bytes.Buffer
	if err := binary.Write(&wire, binary.BigEndian, uint32(ScreenHeaderSize-1)); err != nil {
		t.Fatalf("write length: %v", err)
	}
	if _, err := ReadScreenPacket(&wire); err == nil {
		t.Fatal("ReadScreenPacket accepted packet shorter than its header")
	}
}

func TestReadScreenPacketValidatedRejectsOversizeBeforeHeaderRead(t *testing.T) {
	reader := &screenLengthOnlyReader{}
	binary.BigEndian.PutUint32(reader.length[:], MaxScreenPacket+1)
	admissionCalled := false
	if _, err := ReadScreenPacketValidated(reader, func(*ScreenPacketHeader) ScreenPacketReadDecision {
		admissionCalled = true
		return ScreenPacketRead
	}); err == nil {
		t.Fatal("ReadScreenPacketValidated accepted oversized packet")
	}
	if admissionCalled {
		t.Fatal("oversized packet reached admission callback")
	}
	if reader.readPastLength {
		t.Fatal("oversized packet read beyond its length prefix")
	}
}

func TestScreenAuthRoundTrip(t *testing.T) {
	original := &ScreenAuth{SessionID: 99, Token: "secret-token"}

	var buf bytes.Buffer
	if err := WriteScreenAuth(&buf, original); err != nil {
		t.Fatalf("WriteScreenAuth: %v", err)
	}

	roundTrip, err := ReadScreenAuth(&buf)
	if err != nil {
		t.Fatalf("ReadScreenAuth: %v", err)
	}
	if roundTrip.SessionID != original.SessionID {
		t.Fatalf("SessionID = %d, want %d", roundTrip.SessionID, original.SessionID)
	}
	if roundTrip.Token != original.Token {
		t.Fatalf("Token = %q, want %q", roundTrip.Token, original.Token)
	}
}

func TestScreenFrameMarshalUnmarshal(t *testing.T) {
	original := &ScreenFrame{
		Timestamp: 123456789,
		Width:     1280,
		Height:    720,
		Format:    "jpeg",
		Data:      []byte{1, 2, 3, 4},
	}

	data, err := MarshalScreenFrame(original)
	if err != nil {
		t.Fatalf("MarshalScreenFrame: %v", err)
	}

	roundTrip, err := UnmarshalScreenFrame(data)
	if err != nil {
		t.Fatalf("UnmarshalScreenFrame: %v", err)
	}
	if roundTrip.Timestamp != original.Timestamp {
		t.Fatalf("Timestamp = %d, want %d", roundTrip.Timestamp, original.Timestamp)
	}
	if roundTrip.Width != original.Width || roundTrip.Height != original.Height {
		t.Fatalf("Dimensions = %dx%d, want %dx%d", roundTrip.Width, roundTrip.Height, original.Width, original.Height)
	}
	if roundTrip.Format != original.Format {
		t.Fatalf("Format = %q, want %q", roundTrip.Format, original.Format)
	}
	if !bytes.Equal(roundTrip.Data, original.Data) {
		t.Fatalf("Data = %v, want %v", roundTrip.Data, original.Data)
	}
}

func TestMarshalScreenFrame_RejectsInvalidFormat(t *testing.T) {
	_, err := MarshalScreenFrame(&ScreenFrame{Format: "", Data: []byte{1}})
	if err == nil {
		t.Fatalf("MarshalScreenFrame() error = nil, want non-nil")
	}
}

func TestWriteScreenPacketHandlesShortWrites(t *testing.T) {
	pkt := &ScreenPacket{SessionID: 42, SeqNum: 7, Payload: []byte("ciphertext")}
	w := &shortScreenWriter{maxWrite: 3}
	if err := WriteScreenPacket(w, pkt); err != nil {
		t.Fatalf("WriteScreenPacket: %v", err)
	}
	if w.writes < 2 {
		t.Fatalf("Write calls = %d, want multiple short writes", w.writes)
	}
	got, err := ReadScreenPacket(&w.buf)
	if err != nil {
		t.Fatalf("ReadScreenPacket: %v", err)
	}
	if got.SessionID != pkt.SessionID || got.SeqNum != pkt.SeqNum || !bytes.Equal(got.Payload, pkt.Payload) {
		t.Fatalf("round trip = %#v, want %#v", got, pkt)
	}
}
