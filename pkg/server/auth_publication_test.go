package server

import (
	"crypto/tls"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type publicationFactory struct {
	datastore.DataProviderFactory
	reached, release chan struct{}
	once             sync.Once
	pauseRole        bool
}

func (f *publicationFactory) NonTx() datastore.DataStore {
	return publicationStore{f.DataProviderFactory.NonTx(), f}
}

type publicationStore struct {
	datastore.DataStore
	f *publicationFactory
}

func (s publicationStore) ListChannels() ([]model.Channel, error) {
	if !s.f.pauseRole {
		s.f.once.Do(func() { close(s.f.reached); <-s.f.release })
	}
	return s.DataStore.ListChannels()
}

func (s publicationStore) GetUserByID(id int64) (*model.User, error) {
	if s.f.pauseRole {
		s.f.once.Do(func() { close(s.f.reached); <-s.f.release })
	}
	return s.DataStore.GetUserByID(id)
}

func connectPublicationClient(t *testing.T, srv *Server, handler *ControlHandler, st datastore.DataProviderFactory) *client.ControlClient {
	t.Helper()
	key, err := crypto.GenerateMediaKey(srv.cfg.MediaCipher)
	if err != nil {
		t.Fatal(err)
	}
	if srv.voiceKey == nil {
		srv.voiceKey = key
	}
	certificate := httptest.NewTLSServer(nil)
	t.Cleanup(certificate.Close)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			srv.handleControlConn(handler, conn, st)
		}
	}()
	c, err := client.NewControlClient(listener.Addr().String(), client.SPKIFingerprint(certificate.Certificate()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("control handler did not exit")
		}
	})
	return c
}

func TestBanBeforeAuthenticationPublication(t *testing.T) {
	srv, st, handler := newTestServer(t)
	actorUser, err := st.NonTx().CreateUser("publication-banner", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	target, err := st.NonTx().CreateUser("publication-banned", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(srv.sessions.GetAllByUserIDSnapshots(target.ID)) != 0 {
			t.Error("banned session survived cleanup")
		}
		handler.mu.RLock()
		remaining := len(handler.connMap)
		handler.mu.RUnlock()
		if remaining != 0 {
			t.Error("banned writer remained registered")
		}
		srv.sessionBanMu.Lock()
		checks := len(srv.sessionBanChecks) + len(srv.sessionBanPending)
		srv.sessionBanMu.Unlock()
		if checks != 0 {
			t.Error("ban coordination state leaked")
		}
	})
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("publication-token"), time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &publicationFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	c := connectPublicationClient(t, srv, handler, f)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.release) }) })
	authenticated := make(chan error, 1)
	go func() { _, err := c.Authenticate("publication-token", target.Username); authenticated <- err }()
	select {
	case <-f.reached:
	case <-time.After(time.Second):
		t.Fatal("no publication barrier")
	}
	srv.handleBanUser(handler, actor.ID, &pb.BanUserRequest{UserID: target.ID}, st, &bufferConn{})
	select {
	case err := <-authenticated:
		if err == nil || err.Error() != "auth failed: you have been banned" {
			t.Fatalf("terminal ban was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ban did not reach authenticating client")
	}
	release.Do(func() { close(f.release) })
	// A Ping cannot obtain a usable session after the terminal ban.
	if err := c.Send(&pb.ControlMessage{Ping: &pb.Ping{}}); err == nil {
		// A local buffered write can succeed; the receive loop must still terminate.
		c.StartReceiving()
		select {
		case <-c.Done():
		case <-time.After(time.Second):
			t.Fatal("banned control connection survived")
		}
	}
}

func TestAuthenticationFirstPublicationAfterDemotion(t *testing.T) {
	srv, st, handler := newTestServer(t)
	actorUser, err := st.NonTx().CreateUser("publication-actor", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	target, err := st.NonTx().CreateUser("publication-target", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("publication-token"), time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &publicationFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	c := connectPublicationClient(t, srv, handler, f)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.release) }) })
	type result struct {
		response *pb.AuthResponse
		err      error
	}
	authenticated := make(chan result, 1)
	go func() {
		response, err := c.Authenticate("publication-token", target.Username)
		authenticated <- result{response, err}
	}()
	select {
	case <-f.reached:
	case <-time.After(time.Second):
		t.Fatal("login did not reach channel-list barrier")
	}
	// Registration is already visible, so demotion and bans must find the session.
	response := &bufferConn{}
	srv.handleSetUserRole(handler, actor.ID, &pb.SetUserRoleRequest{TargetUserID: target.ID, NewRole: "user"}, st, response)
	release.Do(func() { close(f.release) })
	select {
	case got := <-authenticated:
		if got.err != nil {
			t.Fatal(got.err)
		}
		session, ok := srv.sessions.GetSnapshot(got.response.SessionID)
		if !ok || session.Role != model.RoleUser || got.response.Role != "user" {
			t.Fatalf("revoked role published: response=%s session=%v", got.response.Role, session.Role)
		}
	case <-time.After(time.Second):
		t.Fatal("authentication stalled")
	}
	updates := make(chan *pb.ServerStateEvent, 1)
	c.SetEventHandler(func(msg *pb.ControlMessage) {
		if msg.ServerStateEvent != nil {
			updates <- msg.ServerStateEvent
		}
	})
	c.StartReceiving()
	srv.broadcastServerState(st, handler)
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("publication gate did not open after authentication")
	}
}

func TestAuthenticationRepairsSuppressedChannelState(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	actorUser, err := st.NonTx().CreateUser("snapshot-actor", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	deleted := &model.Channel{Name: "deleted-during-auth"}
	if err := st.NonTx().CreateChannel(deleted); err != nil {
		t.Fatal(err)
	}
	f := &publicationFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{}), pauseRole: true}
	c := connectPublicationClient(t, srv, handler, f)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.release) }) })
	type result struct {
		auth *pb.AuthResponse
		err  error
	}
	authenticated := make(chan result, 1)
	go func() {
		auth, err := c.Authenticate("", "snapshot-viewer")
		authenticated <- result{auth, err}
	}()
	select {
	case <-f.reached:
	case <-time.After(time.Second):
		t.Fatal("no final role lookup barrier")
	}
	// Both real mutations publish after the AuthResponse snapshot was built.
	srv.handleCreateChannel(actor.ID, &pb.CreateChannelRequest{Name: "created-during-auth"}, st, &bufferConn{}, handler)
	srv.handleDeleteChannel(actor.ID, &pb.DeleteChannelRequest{ChannelID: deleted.ID}, st, &bufferConn{}, handler)
	release.Do(func() { close(f.release) })
	var got result
	select {
	case got = <-authenticated:
	case <-time.After(time.Second):
		t.Fatal("authentication blocked")
	}
	if got.err != nil {
		t.Fatal(got.err) // Authenticate must read AuthResponse first, not a broadcast.
	}
	hasChannel := func(channels []pb.ChannelInfo, name string) bool {
		for _, ch := range channels {
			if ch.Name == name {
				return true
			}
		}
		return false
	}
	if hasChannel(got.auth.Channels, "created-during-auth") || !hasChannel(got.auth.Channels, deleted.Name) {
		t.Fatal("test did not capture the old authentication snapshot")
	}
	events := make(chan *pb.ControlMessage, 4)
	c.SetEventHandler(func(msg *pb.ControlMessage) { events <- msg })
	c.StartReceiving() // No join, Ping or unrelated request repairs this state.
	select {
	case msg := <-events:
		if msg.ServerStateEvent == nil || !hasChannel(msg.ServerStateEvent.Channels, "created-during-auth") || hasChannel(msg.ServerStateEvent.Channels, deleted.Name) {
			t.Fatalf("missing fresh post-authentication channel state: %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("suppressed channel state was never repaired")
	}
}
