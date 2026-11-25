package server

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// Only channel reads fail; all admission and credential operations use the real store.
type channelReadFactory struct {
	datastore.DataProviderFactory
	broken   datastore.DataStore
	failFrom atomic.Int64
	reads    atomic.Int64
}

func (f *channelReadFactory) NonTx() datastore.DataStore {
	return channelReadStore{f.DataProviderFactory.NonTx(), f}
}

type channelReadStore struct {
	datastore.DataStore
	f *channelReadFactory
}

func (s channelReadStore) ListChannels() ([]model.Channel, error) {
	n := s.f.reads.Add(1)
	if from := s.f.failFrom.Load(); from > 0 && n >= from {
		return s.f.broken.ListChannels()
	}
	return s.DataStore.ListChannels()
}
func failingChannelReads(t *testing.T, st datastore.DataProviderFactory) *channelReadFactory {
	t.Helper()
	broken, err := datastore.NewProviderFactory(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.NonTx().ListChannels(); err == nil {
		t.Fatal("closed datastore did not fail")
	}
	f := &channelReadFactory{DataProviderFactory: st, broken: broken.NonTx()}
	f.failFrom.Store(1)
	return f
}

func TestChannelReadFailurePreservesRefreshAndRecovers(t *testing.T) {
	srv, st, h := newTestServer(t)
	h.statePublisher.Do(func() {}) // explicit flush, no timer races
	f := failingChannelReads(t, st)
	c := newControlClient(1, &nopConn{})
	h.connMap[1] = c
	ch := &model.Channel{Name: "last-good"}
	if err := st.NonTx().CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	f.failFrom.Store(0)
	srv.broadcastServerState(f, h)
	srv.flushServerState(f, h)
	good := (<-c.sendQueue).message.ServerStateEvent
	c.pendingState = nil // mirror writeLoop consuming the replaceable snapshot
	if len(good.Channels) != 1 {
		t.Fatal("missing initial state")
	}
	f.failFrom.Store(1)
	// The direct post-auth repair must not enqueue an authoritative empty state.
	srv.sendServerState(f, h, c)
	if len(c.sendQueue) != 0 {
		t.Fatal("failed direct refresh published a snapshot")
	}
	// A failed repair must retain bounded publisher recovery even without a new mutation.
	if !h.stateDirty.Load() {
		t.Error("failed direct repair lost recovery")
	}
	srv.broadcastServerState(f, h)
	srv.flushServerState(f, h)
	if len(c.sendQueue) != 0 || !h.stateDirty.Load() {
		t.Fatal("failed publisher lost last good state or retry")
	}
	response := &bufferConn{}
	srv.handleChannelList(f, response)
	msg, err := protocol.ReadControlMessage(response)
	if err != nil || msg.ErrorResponse == nil || msg.ChannelListResponse != nil {
		t.Fatalf("failed channel request = %#v, %v", msg, err)
	}
	f.failFrom.Store(0)
	srv.flushServerState(f, h)
	if len(c.sendQueue) != 1 {
		t.Fatal("recovery did not publish")
	}
	if got := (<-c.sendQueue).message.ServerStateEvent; len(got.Channels) != 1 || got.Channels[0].ID != ch.ID {
		t.Fatal("recovery lost real channels")
	}
	response = &bufferConn{}
	srv.handleChannelList(f, response)
	msg, err = protocol.ReadControlMessage(response)
	if err != nil || msg.ChannelListResponse == nil || len(msg.ChannelListResponse.Channels) != 1 {
		t.Fatal("channel request did not recover")
	}
}

func TestChannelReadFailureAuthenticationCleanup(t *testing.T) {
	for _, mode := range []string{"personal", "open", "invite", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			srv, st, _ := newTestServer(t)
			var token string
			switch mode {
			case "personal":
				user, err := st.NonTx().CreateUser("reader", model.RoleUser)
				if err != nil {
					t.Fatal(err)
				}
				token = "reader-personal"
				if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken(token), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "open":
				srv.cfg.AllowNoToken = true
			case "invite":
				token = "reader-invite"
				if err := st.NonTx().CreateToken(crypto.HashToken(token), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
			case "bootstrap":
				srv.cfg.DataDir = t.TempDir()
				if err := srv.ensureAdminToken(st); err != nil {
					t.Fatal(err)
				}
				token = readBootstrapToken(t, filepath.Join(srv.cfg.DataDir, bootstrapAdminCredentialName))
			}
			f := failingChannelReads(t, st)
			response := authenticateControl(t, srv, f, "reader", token) // joins handler and checks admission capacity
			if response.ErrorResponse == nil || response.AuthResponse != nil {
				t.Fatal("unavailable initial channels did not reject authentication")
			}
			if mode != "personal" {
				if user, err := st.NonTx().GetUserByUsername("reader"); err != nil || user != nil {
					t.Fatal("failed auth persisted undelivered credentials")
				}
			}
			f.failFrom.Store(0)
			response = authenticateControl(t, srv, f, "reader", token)
			if response.AuthResponse == nil {
				t.Fatal("authentication did not recover with the original credential")
			}
			if mode != "personal" && response.AuthResponse.AutoToken == "" {
				t.Fatal("personal credential was not delivered")
			}
		})
	}
}

func TestChannelReadFailureAfterProvisioningDeliversCredential(t *testing.T) {
	srv, st, h := newTestServer(t)
	h.statePublisher.Do(func() {})
	srv.cfg.AllowNoToken = true
	ch := &model.Channel{Name: "required-snapshot"}
	if err := st.NonTx().CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	f := failingChannelReads(t, st)
	f.failFrom.Store(2) // required read succeeds; refresh after credential commit fails
	c := connectPublicationClient(t, srv, h, f)
	response, err := c.Authenticate("", "provisioned-reader")
	if err != nil || response.AutoToken == "" || len(response.Channels) != 1 {
		t.Fatal("late read failure lost credential or known-good snapshot")
	}
	if f.reads.Load() < 2 || !h.stateDirty.Load() {
		t.Fatal("late read failure did not retain recovery")
	}
	updates := make(chan bool, 1)
	c.SetEventHandler(func(msg *pb.ControlMessage) {
		if msg.ServerStateEvent != nil {
			updates <- len(msg.ServerStateEvent.Channels) == 1
		}
	})
	c.StartReceiving()
	f.failFrom.Store(0)
	srv.flushServerState(f, h)
	select {
	case good := <-updates:
		if !good {
			t.Fatal("recovery published empty channels")
		}
	case <-time.After(time.Second):
		t.Fatal("late read failure was not repaired")
	}
	if got := authenticateControl(t, srv, f, "provisioned-reader", response.AutoToken); got.AuthResponse == nil {
		t.Fatal("delivered credential did not reconnect")
	}
}
