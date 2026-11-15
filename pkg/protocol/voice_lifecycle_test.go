package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestVoiceLifecycleRejectsNilControlMessage(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteControlMessage(&wire, nil); err == nil {
		t.Fatal("nil control message accepted")
	}
	if wire.Len() != 0 {
		t.Fatal("invalid control message wrote a frame")
	}
}

func TestLiveSessionRosterWireValidation(t *testing.T) {
	channels := []pb.ChannelInfo{{ID: 1, Users: []pb.UserInfo{{SessionID: 7}}}}
	oversized := make([]uint32, model.MaxServerSessions+1)
	for i := range oversized {
		oversized[i] = uint32(i + 1)
	}
	for _, tc := range []struct {
		name     string
		ids      []uint32
		channels []pb.ChannelInfo
		valid    bool
	}{
		{"empty", []uint32{}, nil, true},
		{"member and lobby", []uint32{7, 8}, channels, true},
		{"zero", []uint32{0}, nil, false},
		{"reserved", []uint32{VoiceRegistrationMagic}, nil, false},
		{"duplicate", []uint32{7, 7}, nil, false},
		{"oversized", oversized, nil, false},
		{"unlisted member", []uint32{8}, channels, false},
		{"duplicate membership", []uint32{7}, append(channels, channels...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, msg := range []*pb.ControlMessage{
				{AuthResponse: &pb.AuthResponse{LiveSessionIDs: tc.ids, Channels: tc.channels}},
				{ServerStateEvent: &pb.ServerStateEvent{LiveSessionIDs: tc.ids, Channels: tc.channels}},
			} {
				var wire bytes.Buffer
				if err := WriteControlMessage(&wire, msg); (err == nil) != tc.valid {
					t.Fatalf("write valid=%v: %v", tc.valid, err)
				}
				data, err := json.Marshal(msg)
				if err != nil {
					t.Fatal(err)
				}
				wire.Reset()
				if err := binary.Write(&wire, binary.BigEndian, uint32(len(data))); err != nil {
					t.Fatal(err)
				}
				wire.Write(data)
				if _, err := ReadControlMessage(&wire); (err == nil) != tc.valid {
					t.Fatalf("read valid=%v: %v", tc.valid, err)
				}
			}
		})
	}
	if ValidateLiveSessions(nil, nil) == nil {
		t.Fatal("missing lifecycle accepted as authority")
	}
	// Legacy envelopes remain readable; clients must disable voice on nil authority.
	for _, text := range []string{`{"server_state_event":{"channels":[]}}`, `{"auth_response":{"session_id":1}}`} {
		var wire bytes.Buffer
		binary.Write(&wire, binary.BigEndian, uint32(len(text)))
		wire.WriteString(text)
		if _, err := ReadControlMessage(&wire); err != nil {
			t.Fatal(err)
		}
	}
	// The existing nested-JSON contract lets older peers ignore the new field.
	data, _ := json.Marshal(&pb.ServerStateEvent{LiveSessionIDs: []uint32{7}, Channels: channels})
	var legacy struct {
		Channels []pb.ChannelInfo `json:"channels"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil || len(legacy.Channels) != 1 {
		t.Fatalf("legacy decode: %v", err)
	}
}
