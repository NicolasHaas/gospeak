package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestHistoryRequestIdentityWireBounds(t *testing.T) {
	for _, value := range []string{"0", "1", "9223372036854775807", "9223372036854775808", "1.5", "\"1\""} {
		payload := `{"chat_history_request":{"channel_id":1,"request_id":` + value + `}}`
		frame := make([]byte, 4+len(payload))
		binary.BigEndian.PutUint32(frame, uint32(len(payload))) //nolint:gosec // fixed test payloads are far below uint32
		copy(frame[4:], payload)
		result, err := ReadControlMessage(bytes.NewReader(frame))
		valid := value == "0" || value == "1" || value == "9223372036854775807"
		if valid && (err != nil || result.ChatHistoryReq == nil) || !valid && err == nil {
			t.Fatalf("request_id=%s: result=%+v err=%v", value, result, err)
		}
	}
	for _, message := range []*pb.ControlMessage{
		{AuthResponse: &pb.AuthResponse{}},
		{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: 1}},
		{ChatHistoryResp: &pb.ChatHistoryResponse{ChannelID: 1, Messages: []pb.ChatMessage{}}},
	} {
		var wire bytes.Buffer
		if err := WriteControlMessage(&wire, message); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(wire.String(), "request_id") {
			t.Fatal("zero identity/capability changed legacy wire shape")
		}
	}
}
