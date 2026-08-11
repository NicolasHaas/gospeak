package protocol

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type shortWriter struct {
	bytes.Buffer
	max int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.Buffer.Write(p)
}

func TestWriteControlMessageHandlesShortWrites(t *testing.T) {
	want := &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 42}}
	writer := &shortWriter{max: 3}

	if err := WriteControlMessage(writer, want); err != nil {
		t.Fatalf("WriteControlMessage: %v", err)
	}

	got, err := ReadControlMessage(bytes.NewReader(writer.Bytes()))
	if err != nil {
		t.Fatalf("ReadControlMessage: %v", err)
	}
	if got.Ping == nil || got.Ping.Timestamp != want.Ping.Timestamp {
		t.Fatalf("Ping mismatch: got %#v, want timestamp %d", got.Ping, want.Ping.Timestamp)
	}
}

func TestWriteControlMessageRejectsInvalidEnvelopeCardinality(t *testing.T) {
	tests := []struct {
		name string
		msg  *pb.ControlMessage
	}{
		{name: "empty", msg: &pb.ControlMessage{}},
		{
			name: "multiple",
			msg: &pb.ControlMessage{
				Ping: &pb.Ping{Timestamp: 1},
				Pong: &pb.Pong{Timestamp: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := WriteControlMessage(&bytes.Buffer{}, tt.msg); err == nil {
				t.Fatal("WriteControlMessage accepted invalid envelope")
			}
		})
	}
}

func TestReadControlMessageRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "empty", json: `{}`},
		{name: "multiple", json: `{"ping":{},"pong":{}}`},
		{name: "unknown", json: `{"unexpected":{}}`},
		{name: "null", json: `{"ping":null}`},
		{name: "duplicate", json: `{"ping":null,"ping":{}}`},
		{name: "duplicateEscaped", json: `{"ping":{},"\u0070ing":{}}`},
		{name: "notObject", json: `[]`},
		{name: "scalar", json: `42`},
		{name: "stringRoot", json: `"ping"`},
		{name: "nullRoot", json: `null`},
		{name: "malformed", json: `{"ping":`},
		{name: "trailing", json: `{"ping":{}} {"pong":{}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(tt.json)
			frame := make([]byte, 4+len(payload))
			binary.BigEndian.PutUint32(frame[:4], uint32(len(payload))) //nolint:gosec // fixed test payloads are far below uint32
			copy(frame[4:], payload)

			if _, err := ReadControlMessage(bytes.NewReader(frame)); err == nil {
				t.Fatal("ReadControlMessage accepted invalid envelope")
			}
		})
	}
}

func TestControlEnvelopeFieldListMatchesMessageTags(t *testing.T) {
	typ := reflect.TypeOf(pb.ControlMessage{})
	if typ.NumField() != len(controlMessageFields) {
		t.Fatalf("ControlMessage has %d fields, envelope list has %d", typ.NumField(), len(controlMessageFields))
	}
	seenTags := make(map[string]string, typ.NumField())
	seenList := make(map[string]string, len(controlMessageFields))
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("ControlMessage field %s has no usable JSON name %q", field.Name, name)
		}
		if prev, ok := seenTags[name]; ok {
			t.Fatalf("ControlMessage fields %s and %s share JSON name %q", prev, field.Name, name)
		}
		seenTags[name] = field.Name
		if _, ok := controlMessageFields[name]; !ok {
			t.Errorf("ControlMessage field %s with JSON name %q is missing from envelope list", field.Name, name)
		}
	}
	for name := range controlMessageFields {
		if _, ok := seenTags[name]; !ok {
			t.Errorf("envelope list entry %q matches no ControlMessage field", name)
		}
		if prev, ok := seenList[name]; ok {
			t.Fatalf("envelope list contains duplicate entry %q (%s and %s)", name, prev, name)
		}
		seenList[name] = name
	}
}

func TestReadControlMessageRejectsTrailingDuplicateWithEscapedKey(t *testing.T) {
	payload := []byte(`{"ping":{}} {"\u0070ing":{}}`)
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload))) //nolint:gosec // fixed test payload is far below uint32
	copy(frame[4:], payload)

	if _, err := ReadControlMessage(bytes.NewReader(frame)); err == nil {
		t.Fatal("ReadControlMessage accepted trailing duplicate data")
	}
}

func TestValidateControlEnvelopeRejectsTrailingData(t *testing.T) {
	for _, payload := range []string{
		`{"ping":{}} {"pong":{}}`,
		`{"ping":{}} {}`,
		`{"ping":{}}x`,
		`{"ping":{}} 42`,
	} {
		if err := validateControlEnvelope([]byte(payload)); err == nil {
			t.Errorf("validateControlEnvelope accepted trailing data %q", payload)
		}
	}
}

func TestVoicePacketRoundTripPreservesLargeChannelID(t *testing.T) {
	want := &VoicePacket{
		SessionID: 1,
		SeqNum:    2,
		Timestamp: 3,
		ChannelID: 1 << 32,
		Payload:   []byte("encrypted"),
	}

	got, err := UnmarshalVoicePacket(want.Marshal())
	if err != nil {
		t.Fatalf("UnmarshalVoicePacket: %v", err)
	}
	if got.ChannelID != want.ChannelID {
		t.Fatalf("ChannelID = %d, want %d", got.ChannelID, want.ChannelID)
	}
}
