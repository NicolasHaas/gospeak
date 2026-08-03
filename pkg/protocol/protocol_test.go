package protocol

import (
	"bytes"
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
