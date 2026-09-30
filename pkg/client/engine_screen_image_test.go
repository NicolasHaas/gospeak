package client

import (
	"image"
	"testing"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestScreenPacketRejectsImageMismatchAndContinues(t *testing.T) {
	cipher, err := gospeakCrypto.NewVoiceCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	g := newConnectionGeneration()
	defer g.cancel()
	e := NewEngine()
	e.generation, e.state = g, StateConnected
	e.screenCipher = cipher
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 10}
	// Hold the existing callback runner so publication can be checked without sleeps.
	e.callbackQueueRunning = true
	delivered := 0
	e.OnScreenFrame = func(image.Image) { delivered++ }
	data := screenTestJPEG(t, 2, 2)
	send := func(seq uint32, width int32) {
		t.Helper()
		frame, err := protocol.MarshalScreenFrame(&protocol.ScreenFrame{Width: width, Height: 2, Format: "jpeg", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		pkt := &protocol.ScreenPacket{SessionID: 10, SeqNum: seq}
		pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), frame)
		e.handleScreenPacketGeneration(g, pkt)
	}
	send(1, 1)
	if len(e.callbackQueue) != 0 || e.screenReceiveSeq != 1 {
		t.Fatal("invalid authenticated image published or replay sequence not consumed")
	}
	send(2, 2)
	if len(e.callbackQueue) != 1 || e.screenReceiveSeq != 2 {
		t.Fatal("valid image following rejection did not reach callback queue")
	}
	e.callbackQueue[0]()
	if delivered != 1 {
		t.Fatalf("delivered %d frames, want 1", delivered)
	}
}
