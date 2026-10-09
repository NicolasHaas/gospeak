package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestControlDepthRejectsMalformedMaximumFrame(t *testing.T) {
	prefix := `{"auth_request":`
	payload := prefix + strings.Repeat("[", MaxControlMessage-len(prefix))
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, MaxControlMessage)
	copy(frame[4:], payload)
	if _, _, err := ReadControlMessageWithSize(bytes.NewReader(frame)); err == nil || !strings.Contains(err.Error(), "nesting too deep") {
		t.Fatalf("maximum malformed frame: %v", err)
	}
	// Current messages' deepest nested container path is well below the cap.
	if err := validateControlEnvelope([]byte(`{"server_state_event":{"channels":[{"users":[{"username":"Alice"}]}]}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestControlEnvelopeDepthBound(t *testing.T) {
	for _, depth := range []int{32, 33, 128} {
		data := []byte(`{"auth_request":` + strings.Repeat("[", depth-1) + "0" + strings.Repeat("]", depth-1) + "}")
		err := validateControlEnvelope(data)
		if (err == nil) != (depth <= 32) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}
