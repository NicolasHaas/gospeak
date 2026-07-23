package server

import (
	"bytes"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
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

type closeTrackingConn struct {
	nopConn
	once   sync.Once
	closed chan struct{}
}

type bufferConn struct {
	nopConn
	buffer bytes.Buffer
}

func (c *bufferConn) Read(p []byte) (int, error)  { return c.buffer.Read(p) }
func (c *bufferConn) Write(p []byte) (int, error) { return c.buffer.Write(p) }

func newCloseTrackingConn() *closeTrackingConn {
	return &closeTrackingConn{closed: make(chan struct{})}
}

func (c *closeTrackingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func newTestServer(t *testing.T) (*Server, datastore.DataProviderFactory, *ControlHandler) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "gospeak.db")
	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Could not start datastore: %v", err)
	}
	srv := New(cfg, Dependencies{Store: st})
	handler := newControlHandler(srv, st)
	return srv, st, handler
}

func registerTestConn(handler *ControlHandler, sessionID uint32) *closeTrackingConn {
	conn := newCloseTrackingConn()
	handler.setConn(sessionID, conn)
	return conn
}

func requireClosed(t *testing.T, conn *closeTrackingConn) {
	t.Helper()
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("connection was not closed")
	}
}

func TestHandleJoinLeaveChannel(t *testing.T) {
	srv, st, handler := newTestServer(t)
	conn := &nopConn{}

	ch := model.NewChannel()
	if err := st.NonTx().CreateChannel(ch); err != nil {
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

func TestHandleKickUserClosesEverySession(t *testing.T) {
	srv, _, handler := newTestServer(t)
	admin := srv.sessions.Create(1, "admin", model.RoleAdmin)
	targetOne := srv.sessions.Create(2, "target", model.RoleUser)
	targetTwo := srv.sessions.Create(2, "target", model.RoleUser)
	connOne := registerTestConn(handler, targetOne.ID)
	connTwo := registerTestConn(handler, targetTwo.ID)

	srv.handleKickUser(handler, admin.ID, &pb.KickUserRequest{UserID: 2, Reason: "test"}, &nopConn{})

	requireClosed(t, connOne)
	requireClosed(t, connTwo)
}

func TestHandleBanUserClosesEverySession(t *testing.T) {
	srv, st, handler := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("target", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	admin := srv.sessions.Create(adminUser.ID, adminUser.Username, adminUser.Role)
	targetOne := srv.sessions.Create(targetUser.ID, targetUser.Username, targetUser.Role)
	targetTwo := srv.sessions.Create(targetUser.ID, targetUser.Username, targetUser.Role)
	connOne := registerTestConn(handler, targetOne.ID)
	connTwo := registerTestConn(handler, targetTwo.ID)

	srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{UserID: targetUser.ID, Reason: "test"}, st, &nopConn{})

	requireClosed(t, connOne)
	requireClosed(t, connTwo)
}

func TestHandleSetUserRoleUpdatesEverySession(t *testing.T) {
	srv, st, handler := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("target", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	admin := srv.sessions.Create(adminUser.ID, adminUser.Username, adminUser.Role)
	targetOne := srv.sessions.Create(targetUser.ID, targetUser.Username, targetUser.Role)
	targetTwo := srv.sessions.Create(targetUser.ID, targetUser.Username, targetUser.Role)

	srv.handleSetUserRole(handler, admin.ID, &pb.SetUserRoleRequest{TargetUserID: targetUser.ID, NewRole: model.RoleUser.String()}, st, &nopConn{})

	for _, sessionID := range []uint32{targetOne.ID, targetTwo.ID} {
		snapshot, ok := srv.sessions.GetSnapshot(sessionID)
		if !ok {
			t.Fatalf("session %d not found", sessionID)
		}
		if snapshot.Role != model.RoleUser {
			t.Errorf("session %d role = %s, want %s", sessionID, snapshot.Role, model.RoleUser)
		}
	}

	responseConn := &bufferConn{}
	srv.handleCreateToken(targetTwo.ID, &pb.CreateTokenRequest{Role: model.RoleUser.String()}, st, responseConn)
	response, err := protocol.ReadControlMessage(responseConn)
	if err != nil {
		t.Fatalf("ReadControlMessage: %v", err)
	}
	if response.ErrorResponse == nil || response.ErrorResponse.Code != 30 {
		t.Fatalf("demoted second session received %#v, want permission error", response)
	}
}
