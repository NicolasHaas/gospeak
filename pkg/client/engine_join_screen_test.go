package client

import (
	"bytes"
	"context"
	"image"
	"testing"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChannelJoinScreenState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel int64
		stale   bool
	}{
		{name: "same channel", channel: 1},
		{name: "different channel", channel: 2},
		{name: "stale generation", channel: 2, stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine()
			g := newConnectionGeneration()
			defer g.cancel()
			g.control = &ControlClient{conn: &recordingConn{}}
			g.voice = &VoiceClient{channelID: 1}
			e.generation, e.state, e.channelID = g, StateConnected, 1
			key := bytes.Repeat([]byte{1}, 16)
			cipher, err := gospeakCrypto.NewVoiceCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			share := &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1}
			ctx, cancel := context.WithCancel(g.ctx)
			defer cancel()
			e.screenMu.Lock()
			e.activeScreenShare, e.screenCipher, e.screenKey = share, cipher, key
			e.screenReceiveSeq, e.screenSeqNum = 7, 9
			e.screenShareRunning, e.screenShareCancel = true, cancel
			e.screenMu.Unlock()
			joined, cleared := 0, 0
			e.OnChannelJoined = func(int64) { joined++ }
			e.OnScreenShareEvent = func(*pb.ScreenShareEvent) { cleared++ }
			e.OnScreenFrame = func(image.Image) { cleared++ }
			if err := e.JoinChannel(tc.channel); err != nil {
				t.Fatal(err)
			}
			delivery := g
			if tc.stale {
				delivery = newConnectionGeneration()
				defer delivery.cancel()
				delivery.pendingJoins[tc.channel] = 1
			}
			e.handleEventGeneration(delivery, &pb.ControlMessage{
				ChannelJoinResponse: &pb.ChannelJoinResponse{ChannelID: tc.channel, Success: true},
			})
			barrier := make(chan struct{})
			e.invokeCallback(func() { close(barrier) })
			waitForSignal(t, barrier, "join callbacks did not complete")
			changed := tc.channel != 1 && !tc.stale
			e.screenMu.Lock()
			if changed {
				if e.activeScreenShare != nil || e.screenCipher != nil || e.screenKey != nil || e.screenReceiveSeq != 0 || e.screenShareRunning || ctx.Err() == nil || cleared != 2 {
					t.Error("channel change did not clear screen state, cancel capture and notify viewers")
				}
			} else if e.activeScreenShare != share || e.screenCipher != cipher || !bytes.Equal(e.screenKey, key) || e.screenReceiveSeq != 7 || !e.screenShareRunning || ctx.Err() != nil || cleared != 0 {
				t.Error("unchanged channel or stale ACK cleared screen state or stopped capture")
			}
			if e.screenSeqNum != 9 {
				t.Error("join reset generation-owned outgoing screen sequence")
			}
			e.screenMu.Unlock()
			wantChannel, wantJoined, wantPending := tc.channel, 1, uint32(0)
			if tc.stale {
				wantChannel, wantJoined, wantPending = 1, 0, 1
			}
			g.mu.Lock()
			pending := g.pendingJoins[tc.channel]
			g.mu.Unlock()
			if e.GetChannelID() != wantChannel || g.voice.channelID != uint64(wantChannel) || joined != wantJoined || pending != wantPending {
				t.Errorf("ACK bookkeeping: engine=%d voice=%d callbacks=%d pending=%d", e.GetChannelID(), g.voice.channelID, joined, pending)
			}
		})
	}
}
