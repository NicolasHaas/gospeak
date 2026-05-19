package protocol

import (
	"bytes"
	"testing"
)

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
