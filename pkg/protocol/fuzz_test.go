package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzReadControlMessage(f *testing.F) {
	for _, payload := range []string{
		`{"ping":{"timestamp":42}}`,
		`{"chat_message":{"channel_id":1,"text":"hello"}}`,
		`{"chat_message":{"channel_id":1,"text":"héllo 👋\n\u0000"}}`,
		`{"ping":{},"pong":{}}`,
		`{"ping":null}`,
		`{"ping":{},"\u0070ing":{}}`,
		`{"ping":`,
	} {
		frame := make([]byte, 4+len(payload))
		binary.BigEndian.PutUint32(frame, uint32(len(payload))) //nolint:gosec // fixed seed strings fit uint32
		copy(frame[4:], payload)
		f.Add(frame)
	}
	f.Add([]byte{0, 8, 0, 1})      // oversized length, no payload
	f.Add([]byte{0, 0, 0, 8, '{'}) // truncated payload

	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) > MaxControlMessage+4 {
			return
		}
		msg, _, err := ReadControlMessageWithSize(bytes.NewReader(frame))
		if err != nil {
			return
		}
		var encoded bytes.Buffer
		if err := WriteControlMessage(&encoded, msg); err != nil {
			t.Fatalf("accepted message could not be written: %v", err)
		}
		if _, err := ReadControlMessage(&encoded); err != nil {
			t.Fatalf("written message could not be read: %v", err)
		}
	})
}

func FuzzReadScreenPacket(f *testing.F) {
	var valid bytes.Buffer
	if err := WriteScreenPacket(&valid, &ScreenPacket{SessionID: 1, SeqNum: 2, Payload: []byte("ciphertext")}); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}) // oversized length
	f.Add([]byte{0, 0, 0, 9, 1})          // truncated header
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 64*1024 {
			return
		}
		packet, err := ReadScreenPacket(bytes.NewReader(wire))
		if err != nil {
			return
		}
		var encoded bytes.Buffer
		if err := WriteScreenPacket(&encoded, packet); err != nil {
			t.Fatalf("accepted packet could not be written: %v", err)
		}
		again, err := ReadScreenPacket(&encoded)
		if err != nil || again.SessionID != packet.SessionID || again.SeqNum != packet.SeqNum || !bytes.Equal(again.Payload, packet.Payload) {
			t.Fatalf("screen packet round trip: %+v, %v", again, err)
		}
	})
}

func FuzzUnmarshalScreenFrame(f *testing.F) {
	valid, err := MarshalScreenFrame(&ScreenFrame{Timestamp: 1, Width: 640, Height: 480, Format: "jpeg", Data: []byte("image")})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{0, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		frame, err := UnmarshalScreenFrame(data)
		if err != nil {
			return
		}
		encoded, err := MarshalScreenFrame(frame)
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("screen frame round trip: %v", err)
		}
	})
}
