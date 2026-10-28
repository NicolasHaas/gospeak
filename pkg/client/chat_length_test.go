package client

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestSendChatLength(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.control = &ControlClient{conn: conn}
	e.generation, e.state = g, StateConnected
	for _, runeText := range []string{"a", "界"} {
		text := strings.Repeat(runeText, model.MessageMaxBodyLength)
		conn.b.Reset()
		if err := e.SendChat(9, text+runeText); !errors.Is(err, model.ErrMessageBodyTooLong) {
			t.Errorf("overlength %q: %v", runeText, err)
		}
		if conn.b.Len() != 0 {
			t.Error("refused message reached the wire")
		}
		conn.b.Reset()
		// Match the server's trim/sanitize-before-count boundary without
		// changing the payload that the server remains responsible for cleaning.
		text = " \t" + text + "\x00\u202e "
		if err := e.SendChat(9, text); err != nil {
			t.Fatal(err)
		}
		msg, err := protocol.ReadControlMessage(bytes.NewReader(conn.b.Bytes()))
		if err != nil || msg.ChatMsg == nil || msg.ChatMsg.Text != text || msg.ChatMsg.ChannelID != 9 {
			t.Fatalf("boundary message changed: %v, %v", msg, err)
		}
	}
}
