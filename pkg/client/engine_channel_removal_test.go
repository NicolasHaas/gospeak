package client

import (
	"context"
	"testing"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestServerStateChannelRemoval(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	defer g.cancel()
	conn := &recordingConn{}
	g.control = &ControlClient{conn: conn}
	g.voice = &VoiceClient{channelID: 1}
	e.generation, e.state, e.channelID = g, StateConnected, 1
	e.mediaCipher = "aes128"
	capture, cancel := context.WithCancel(g.ctx)
	defer cancel()
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, ChannelID: 1}
	e.screenKey = make([]byte, 16)
	cipher, err := gospeakCrypto.NewVoiceCipher(e.screenKey)
	if err != nil {
		t.Fatal(err)
	}
	e.screenCipher = cipher
	e.screenReceiveSeq, e.screenSeqNum = 7, 9
	e.screenShareRunning, e.screenShareCancel = true, cancel
	updates, clears := 0, 0
	e.OnChannelsUpdate = func([]pb.ChannelInfo) { updates++ }
	e.OnScreenShareEvent = func(*pb.ScreenShareEvent) { clears++ }
	flush := func() {
		t.Helper()
		barrier := make(chan struct{})
		e.invokeCallback(func() { close(barrier) })
		waitForSignal(t, barrier, "state callbacks did not complete")
	}
	state := func(channels ...pb.ChannelInfo) *pb.ControlMessage {
		return &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{Channels: channels, ScreenShareEnabled: true}}
	}
	// A pending move must survive eviction of the old channel.
	if err := e.JoinChannel(2); err != nil {
		t.Fatal(err)
	}
	e.handleEventGeneration(g, state(pb.ChannelInfo{ID: 1}, pb.ChannelInfo{ID: 2}))
	flush()
	if e.GetChannelID() != 1 || g.voice.channelID != 1 || capture.Err() != nil || clears != 0 {
		t.Fatal("ordinary snapshot changed voice or screen state")
	}
	e.handleEventGeneration(g, state(pb.ChannelInfo{ID: 2}))
	flush()
	if e.GetChannelID() != 0 || g.voice.channelID != 0 {
		t.Errorf("removed channel retained: engine=%d voice=%d", e.GetChannelID(), g.voice.channelID)
	}
	if e.activeScreenShare != nil || e.screenCipher != nil || e.screenKey != nil || e.screenReceiveSeq != 0 || e.screenShareRunning || capture.Err() == nil || clears != 1 {
		t.Error("removed channel retained screen state or capture")
	}
	if e.screenSeqNum != 9 || updates != 2 || e.GetState() != StateConnected {
		t.Error("eviction reset generation state or lost channel-list update")
	}
	// A captured start response may be enqueued after the deletion snapshot.
	late := &pb.ControlMessage{ScreenShareEvent: &pb.ScreenShareEvent{
		Active: true, SessionID: 10, ChannelID: 1, MediaCipher: "aes128", EncryptionKey: make([]byte, 16),
	}}
	e.handleEventGeneration(g, late)
	flush()
	if e.activeScreenShare != nil || e.screenCipher != nil || e.screenKey != nil || clears != 1 {
		t.Fatal("late screen event restored evicted media state or notified the UI")
	}
	// The later ACK is authoritative; subsequent snapshots cannot undo this join.
	e.handleEventGeneration(g, &pb.ControlMessage{ChannelJoinResponse: &pb.ChannelJoinResponse{ChannelID: 2, Success: true}})
	e.handleEventGeneration(g, state(pb.ChannelInfo{ID: 2}))
	flush()
	if e.GetChannelID() != 2 || g.voice.channelID != 2 {
		t.Fatal("eviction invalidated the later join")
	}
	clearsBefore := clears
	e.handleEventGeneration(g, late)
	flush()
	if e.activeScreenShare != nil || clears != clearsBefore {
		t.Fatal("old-channel screen event survived a later join")
	}
	current := *late.ScreenShareEvent
	current.ChannelID = 2
	e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: &current})
	flush()
	if e.activeScreenShare == nil || e.screenCipher == nil || clears != clearsBefore+1 {
		t.Fatal("later channel's valid screen event was rejected")
	}
	stale := newConnectionGeneration()
	defer stale.cancel()
	e.handleEventGeneration(stale, state())
	flush()
	if e.GetChannelID() != 2 {
		t.Fatal("stale generation evicted current membership")
	}
	// Removing the last channel leaves voice unjoined, without a fallback request.
	written := conn.b.Len()
	e.handleEventGeneration(g, state())
	flush()
	if e.GetChannelID() != 0 || g.voice.channelID != 0 || conn.b.Len() != written {
		t.Fatal("last-channel removal retained voice or sent an automatic join")
	}
}
