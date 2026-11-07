package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChannelImportRejectsOversizedWireStateAtomically(t *testing.T) {
	st := newChannelImportStore(t)
	var input strings.Builder
	input.WriteString("channels:\n")
	for i := range 256 {
		fmt.Fprintf(&input, "  - name: '%s%03d'\n    description: '%s'\n", strings.Repeat("<", 61), i, strings.Repeat("&", 256))
	}
	if err := ImportChannelsFromYAML([]byte(input.String()), st); err == nil {
		t.Fatal("oversized channel import accepted")
	}
	assertNoImportedChannels(t, st)
}

func TestMaximumAcceptedStateFitsWireFrames(t *testing.T) {
	if model.MaxChannelStateBytes+model.MaxServerSessions*model.StateUserBytes+model.StateEnvelopeBytes != protocol.MaxControlMessage {
		t.Fatal("wire reservation no longer matches frame limit")
	}
	// Constituent bounds prove all accepted mixtures fit, not just this fixture.
	worstUser := pb.UserInfo{SessionID: math.MaxUint32, ID: math.MaxInt64, Username: strings.Repeat("𐐀", model.MaxUsernameLength), Role: "moderator"}
	data, err := json.Marshal(worstUser)
	if err != nil || len(data)+1 > model.StateUserBytes {
		t.Fatalf("user bound: %d, %v", len(data)+1, err)
	}
	for _, text := range []string{"&", "𐐀", "\u2028"} {
		ch := model.Channel{ID: math.MaxInt64, ParentID: math.MaxInt64, Name: "x" + strings.Repeat(text, model.MaxChannelNameLength-1), Description: strings.Repeat(text, model.MaxChannelDescLength), MaxUsers: model.MaxChannelUsers}
		if err := ch.Validate(); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(pb.ChannelInfo{ID: ch.ID, ParentID: ch.ParentID, Name: ch.Name, Description: ch.Description, MaxUsers: model.MaxChannelUsers, Users: []pb.UserInfo{}})
		if err != nil || len(data)+1 > model.ChannelStateBytes(ch) {
			t.Fatalf("channel bound: %d, %v", len(data)+1, err)
		}
	}
	st := newChannelImportStore(t)
	remaining := model.MaxChannelStateBytes
	var firstID int64
	for i := 0; remaining >= model.StateChannelFixedBytes+6*(model.MaxChannelNameLength+model.MaxChannelDescLength); i++ {
		ch := &model.Channel{Name: strings.Repeat("<", 60) + fmt.Sprintf("%04d", i), Description: strings.Repeat("&", 256)}
		if err := st.NonTx().CreateChannel(ch); err != nil {
			t.Fatal(err)
		}
		firstID = ch.ID
		remaining -= model.ChannelStateBytes(*ch)
	}
	// Fill the remaining reservation exactly, then test the first rejected write.
	last := &model.Channel{Name: strings.Repeat("x", (remaining-model.StateChannelFixedBytes)/6)}
	if err := st.NonTx().CreateChannel(last); err != nil {
		t.Fatal(err)
	}
	remaining -= model.ChannelStateBytes(*last)
	if remaining != 0 {
		t.Fatalf("fixture did not hit exact budget: remainder %d", remaining)
	}
	before, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	for _, temporary := range []bool{false, true} {
		if err := st.NonTx().CreateChannel(&model.Channel{Name: "over", IsTemp: temporary, ParentID: firstID}); err == nil {
			t.Fatal("over-budget create succeeded")
		}
	}
	after, err := st.NonTx().ListChannels()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected writes changed channels: %v", err)
	}
	srv := New(DefaultConfig(), Dependencies{Store: st})
	defer srv.Shutdown()
	for i := range model.MaxServerSessions {
		name := strings.Repeat("𐐀", 28) + fmt.Sprintf("%04d", i)
		sess, err := srv.sessions.Create(int64(math.MaxInt64-i), name, model.RoleModerator, fmt.Sprintf("192.0.%d.%d", i/64, i%64+1))
		if err != nil {
			t.Fatal(err)
		}
		srv.channels.Join(sess.ID, firstID)
	}
	infos := srv.buildChannelInfos(before)
	auth := &pb.AuthResponse{SessionID: math.MaxUint32, Username: worstUser.Username, Role: "moderator", ChannelScope: math.MaxInt64, EncryptionKey: bytes.Repeat([]byte{255}, 32), VoiceRegistrationKey: bytes.Repeat([]byte{255}, 32), MediaCipher: "chacha20", ScreenAddr: strings.Repeat("&", 256), ScreenAuthToken: strings.Repeat("f", 64), AutoToken: strings.Repeat("f", 64), ScreenShareEnabled: true, HistoryRequestIDs: true}
	envelope, err := json.Marshal(&pb.ControlMessage{AuthResponse: auth})
	if err != nil || len(envelope) > model.StateEnvelopeBytes {
		t.Fatalf("auth envelope bound: %d, %v", len(envelope), err)
	}
	auth.Channels = infos
	for _, msg := range []*pb.ControlMessage{{AuthResponse: auth}, {ServerStateEvent: &pb.ServerStateEvent{Channels: infos, ScreenShareEnabled: true}}} {
		var wire bytes.Buffer
		if err := protocol.WriteControlMessage(&wire, msg); err != nil {
			t.Fatal(err)
		}
		t.Logf("maximum accepted state channels=%d sessions=%d frame bytes=%d", len(infos), model.MaxServerSessions, wire.Len()-4)
	}
}

func TestAccumulatedChannelImportsRejectAtomically(t *testing.T) {
	st := newChannelImportStore(t)
	var input strings.Builder
	input.WriteString("channels:\n")
	for i := range 256 {
		fmt.Fprintf(&input, "  - name: 'first-%d'\n", i)
	}
	if err := ImportChannelsFromYAML([]byte(input.String()), st); err != nil {
		t.Fatal(err)
	}
	before, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	input.Reset()
	input.WriteString("channels:\n")
	for i := range 128 {
		fmt.Fprintf(&input, "  - name: 'second-%d'\n    description: '%s'\n", i, strings.Repeat("&", 256))
	}
	if err := ImportChannelsFromYAML([]byte(input.String()), st); err == nil {
		t.Fatal("accumulated over-budget import succeeded")
	}
	after, err := st.NonTx().ListChannels()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed import partially persisted: %v", err)
	}
}

func TestLegacyChannelStateFailsBeforeStartupMutation(t *testing.T) {
	st := newChannelImportStore(t)
	for i := range 256 {
		if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO channels(name, description) VALUES (?, ?)`, fmt.Sprintf("legacy-%d", i), strings.Repeat("&", 256)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(DefaultConfig(), Dependencies{Store: st})
	defer srv.Shutdown()
	if err := srv.Run(); err == nil || !strings.Contains(err.Error(), "serialized state budget") {
		t.Fatalf("legacy startup: %v", err)
	}
	after, err := st.NonTx().ListChannels()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy state changed: %v", err)
	}
}

func TestStateConfigBoundsBeforeInitialization(t *testing.T) {
	for _, screen := range []bool{false, true} {
		st := newChannelImportStore(t)
		cfg := DefaultConfig()
		if screen {
			cfg.EnableScreenShare = true
			cfg.ScreenAddr = strings.Repeat("a", 257)
		} else {
			cfg.MaxSessions = model.MaxServerSessions + 1
		}
		srv := New(cfg, Dependencies{Store: st})
		defer srv.Shutdown()
		if err := srv.Run(); err == nil {
			t.Fatal("over-limit config accepted")
		}
		assertNoImportedChannels(t, st)
	}
}
