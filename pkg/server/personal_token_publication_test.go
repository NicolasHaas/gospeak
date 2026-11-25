package server

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type credentialReadFailureFactory struct {
	datastore.DataProviderFactory
	failVersion bool
	lookups     atomic.Int32
}

func (f *credentialReadFailureFactory) NonTx() datastore.DataStore {
	return credentialReadFailureStore{f.DataProviderFactory.NonTx(), f}
}

type credentialReadFailureStore struct {
	datastore.DataStore
	f *credentialReadFailureFactory
}

func (s credentialReadFailureStore) GetUserByPersonalTokenHash(hash string) (*model.User, error) {
	if !s.f.failVersion && s.f.lookups.Add(1) > 1 {
		return nil, errors.New("injected credential read failure")
	}
	return s.DataStore.GetUserByPersonalTokenHash(hash)
}
func (s credentialReadFailureStore) PersonalTokenVersion(id int64) (int64, error) {
	if s.f.failVersion {
		return 0, errors.New("injected version read failure")
	}
	return s.DataStore.PersonalTokenVersion(id)
}
func TestPersonalTokenFinalReadFailuresAreNotCredentialRejection(t *testing.T) {
	for _, version := range []bool{false, true} {
		t.Run(map[bool]string{false: "final-hash", true: "publication-version"}[version], func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			u, err := st.NonTx().CreateUser("credential-read-failure", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.NonTx().UpdateUserPersonalToken(u.ID, crypto.HashToken("personal"), time.Now()); err != nil {
				t.Fatal(err)
			}
			serverConn, peer := newControlPipe()
			_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.handleControlConn(handler, serverConn, &credentialReadFailureFactory{DataProviderFactory: st, failVersion: version})
			}()
			t.Cleanup(func() {
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(4 * time.Second):
					t.Error("credential failure handler did not join")
				}
			})
			if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Token: "personal", Username: u.Username, MediaCiphers: []string{"aes128"}}}); err != nil {
				t.Fatal(err)
			}
			response, err := protocol.ReadControlMessage(peer)
			if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Code != 3 {
				t.Fatal("internal read failure was reported as invalid credential or success")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("credential failure did not retire prepared session")
			}
			if srv.sessions.Count() != 0 || srv.sessions.CapacitySnapshot().CapacityUsed != 0 {
				t.Fatal("credential read failure leaked session capacity")
			}
		})
	}
}

// Pause after the original lookup, before any ban coordination or activation.
type credentialLookupFactory struct {
	datastore.DataProviderFactory
	reached, release chan struct{}
	once             sync.Once
}

func (f *credentialLookupFactory) NonTx() datastore.DataStore {
	return credentialLookupStore{f.DataProviderFactory.NonTx(), f}
}

type credentialLookupStore struct {
	datastore.DataStore
	f *credentialLookupFactory
}

func (s credentialLookupStore) GetUserByPersonalTokenHash(hash string) (*model.User, error) {
	user, err := s.DataStore.GetUserByPersonalTokenHash(hash)
	s.f.once.Do(func() { close(s.f.reached); <-s.f.release })
	return user, err
}

func TestPersonalTokenReplacementBeforeActivationRejectsStaleLookup(t *testing.T) {
	srv, st, handler := newTestServer(t)
	target, err := st.NonTx().CreateUser("credential-lookup", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("old-personal"), time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &credentialLookupFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	c := connectPublicationClient(t, srv, handler, f)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.release) }) })
	result := make(chan error, 1)
	go func() { _, err := c.Authenticate("old-personal", target.Username); result <- err }()
	select {
	case <-f.reached:
	case <-time.After(time.Second):
		t.Fatal("no credential lookup barrier")
	}
	_, actorConn := personalMutationActor(t, srv, st, handler, "publication-admin", model.RoleAdmin, false)
	version, err := st.NonTx().PersonalTokenVersion(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := personalMutation(t, srv, st, handler, actorConn, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}})
	if response.RevokePersonalTokenResp.Error != "" {
		t.Fatal(response.RevokePersonalTokenResp.Error)
	}
	release.Do(func() { close(f.release) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("stale personal credential authenticated after replacement")
		}
	case <-time.After(time.Second):
		t.Fatal("authentication stalled")
	}
	if got := srv.sessions.GetAllByUserIDSnapshots(target.ID); len(got) != 0 {
		t.Fatal("stale credential activated a session")
	}
}

func TestPersonalTokenReplacementBeforeFirstPublicationRejectsStaleSession(t *testing.T) {
	srv, st, handler := newTestServer(t)
	target, err := st.NonTx().CreateUser("credential-publication", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("old-personal"), time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &publicationFactory{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	c := connectPublicationClient(t, srv, handler, f)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.release) }) })
	result := make(chan error, 1)
	go func() { _, err := c.Authenticate("old-personal", target.Username); result <- err }()
	select {
	case <-f.reached:
	case <-time.After(time.Second):
		t.Fatal("no first publication barrier")
	}
	if len(srv.sessions.GetAllByUserIDSnapshots(target.ID)) != 1 {
		t.Fatal("session was not activated before barrier")
	}
	_, actorConn := personalMutationActor(t, srv, st, handler, "publication-admin", model.RoleAdmin, false)
	version, err := st.NonTx().PersonalTokenVersion(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := personalMutation(t, srv, st, handler, actorConn, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}})
	if response.RevokePersonalTokenResp.Error != "" {
		t.Fatal(response.RevokePersonalTokenResp.Error)
	}
	release.Do(func() { close(f.release) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("stale personal credential published success after replacement")
		}
	case <-time.After(time.Second):
		t.Fatal("authentication stalled")
	}
}
