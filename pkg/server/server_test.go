package server

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/store"
)

type nopConn struct{}

func (c *nopConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *nopConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *nopConn) Close() error                       { return nil }
func (c *nopConn) LocalAddr() net.Addr                { return &net.IPAddr{} }
func (c *nopConn) RemoteAddr() net.Addr               { return &net.IPAddr{} }
func (c *nopConn) SetDeadline(_ time.Time) error      { return nil }
func (c *nopConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *nopConn) SetWriteDeadline(_ time.Time) error { return nil }

func newTestServer(t *testing.T) (*Server, store.DataStore, *ControlHandler) {
	t.Helper()
	st := store.NewMemory()
	cfg := DefaultConfig()
	srv := New(cfg, Dependencies{Store: st})
	handler := newControlHandler(srv, st)
	return srv, st, handler
}

func TestHandleJoinLeaveChannel(t *testing.T) {
	srv, st, handler := newTestServer(t)
	conn := &nopConn{}

	ch := model.NewChannel()
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	session := srv.sessions.Create(1, "johndoe", model.RoleUser)

	srv.handleJoinChannel(handler, session.ID, &pb.JoinChannelRequest{ChannelID: ch.ID}, st, conn)
	joinedChannel := srv.channels.ChannelOf(session.ID)
	if joinedChannel != ch.ID {
		t.Fatalf("JoinChannel: expected channel %d got %d", ch.ID, joinedChannel)
	}

	snap, ok := srv.sessions.GetSnapshot(session.ID)
	if !ok {
		t.Fatalf("GetSnapshot: missing session")
	}
	if snap.ChannelID != ch.ID {
		t.Fatalf("JoinChannel: session channel mismatch want=%d got=%d", ch.ID, snap.ChannelID)
	}

	srv.handleLeaveChannel(handler, session.ID, st, conn)
	leftChannel := srv.channels.ChannelOf(session.ID)
	if leftChannel != 0 {
		t.Fatalf("LeaveChannel: expected channel 0 got %d", leftChannel)
	}

	snap, ok = srv.sessions.GetSnapshot(session.ID)
	if !ok {
		t.Fatalf("GetSnapshot: missing session")
	}
	if snap.ChannelID != 0 {
		t.Fatalf("LeaveChannel: session channel mismatch want=0 got=%d", snap.ChannelID)
	}
}

func TestHandleUserState(t *testing.T) {
	srv, st, handler := newTestServer(t)

	session := srv.sessions.Create(1, "johndoe", model.RoleUser)

	srv.handleUserState(handler, session.ID, &pb.UserStateUpdate{Muted: true, Deafened: true}, st)

	snap, ok := srv.sessions.GetSnapshot(session.ID)
	if !ok {
		t.Fatalf("GetSnapshot: missing session")
	}
	if !snap.Muted || !snap.Deafened {
		t.Fatalf("HandleUserState: expected muted/deafened true, got muted=%t deafened=%t", snap.Muted, snap.Deafened)
	}
}
