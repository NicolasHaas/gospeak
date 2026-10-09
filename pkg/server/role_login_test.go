package server

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type roleLoginFactory struct {
	datastore.DataProviderFactory
	reached, release chan struct{}
}

func (f roleLoginFactory) NonTx() datastore.DataStore {
	return roleLoginStore{DataStore: f.DataProviderFactory.NonTx(), reached: f.reached, release: f.release}
}

type roleLoginStore struct {
	datastore.DataStore
	reached, release chan struct{}
}

func (s roleLoginStore) IsUserBanned(id int64) (bool, error) {
	banned, err := s.DataStore.IsUserBanned(id)
	close(s.reached)
	<-s.release
	return banned, err
}

type roleRefreshFailureFactory struct {
	datastore.DataProviderFactory
	err error
}

func (f roleRefreshFailureFactory) NonTx() datastore.DataStore {
	return roleRefreshFailureStore{DataStore: f.DataProviderFactory.NonTx(), err: f.err}
}

type roleRefreshFailureStore struct {
	datastore.DataStore
	err error
}

func (s roleRefreshFailureStore) GetUserByID(int64) (*model.User, error) { return nil, s.err }

func TestLoginRoleRefreshFailsClosed(t *testing.T) {
	for _, lookupErr := range []error{nil, errors.New("lookup failed")} {
		t.Run(fmt.Sprint(lookupErr), func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			srv.cfg.AllowNoToken = true
			serverConn, peer := net.Pipe()
			_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan struct{})
			go func() {
				srv.handleControlConn(handler, serverConn, roleRefreshFailureFactory{st, lookupErr})
				close(done)
			}()
			t.Cleanup(func() { _ = peer.Close(); <-done })
			if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "refresh-failure", MediaCiphers: []string{"aes128"}}}); err != nil {
				t.Fatal(err)
			}
			response, err := protocol.ReadControlMessage(peer)
			if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Code != 3 || response.ErrorResponse.Message != "internal error" {
				t.Fatalf("refresh failure: %#v, %v", response, err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("failed refresh retained its handler")
			}
			if srv.sessions.Count() != 0 || srv.sessions.CapacitySnapshot().CapacityUsed != 0 {
				t.Fatal("failed role refresh retained session capacity")
			}
		})
	}
}

func TestLoginCannotPublishRoleReadBeforeDemotion(t *testing.T) {
	srv, st, handler := newTestServer(t)
	actorUser, err := st.NonTx().CreateUser("demoter", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	target, err := st.NonTx().CreateUser("racing-admin", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("role-login-token"), time.Now()); err != nil {
		t.Fatal(err)
	}
	probe := roleLoginFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	serverConn, peer := net.Pipe()
	_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() { srv.handleControlConn(handler, serverConn, probe); close(done) }()
	released := false
	t.Cleanup(func() {
		if !released {
			close(probe.release)
		}
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("login handler did not stop")
		}
	})
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: target.Username, Token: "role-login-token", MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.reached:
	case <-time.After(time.Second):
		t.Fatal("login did not reach prepared-session barrier")
	}
	// Real role handler commits while the new login is prepared but not active.
	responseConn := &bufferConn{}
	srv.handleSetUserRole(handler, actor.ID, &pb.SetUserRoleRequest{TargetUserID: target.ID, NewRole: "user"}, st, responseConn)
	response, err := protocol.ReadControlMessage(responseConn)
	close(probe.release)
	released = true
	if err != nil || response.SetUserRoleResp == nil || !response.SetUserRoleResp.Success {
		t.Fatalf("demotion: %#v, %v", response, err)
	}
	response, err = protocol.ReadControlMessage(peer)
	if err != nil || response.AuthResponse == nil {
		t.Fatalf("login: %#v, %v", response, err)
	}
	if response.AuthResponse.Role != "user" {
		t.Fatalf("AuthResponse retained revoked role %q", response.AuthResponse.Role)
	}
	session, ok := srv.sessions.GetSnapshot(response.AuthResponse.SessionID)
	if !ok || session.Role != model.RoleUser {
		t.Fatalf("active login retained revoked role: %#v", session)
	}
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{CreateTokenReq: &pb.CreateTokenRequest{Role: "admin"}}); err != nil {
		t.Fatal(err)
	}
	response, err = protocol.ReadControlMessage(peer)
	if err != nil || response.ErrorResponse == nil {
		t.Fatalf("demoted login minted token: %#v, %v", response, err)
	}
}
