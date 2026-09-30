package server

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type screenIPBanFactory struct {
	datastore.DataProviderFactory
	banned    bool
	protected bool
	err       error
	checked   chan string
}
type screenIPBanStore struct {
	datastore.DataStore
	factory screenIPBanFactory
}

func (f screenIPBanFactory) NonTx() datastore.DataStore { return screenIPBanStore{factory: f} }
func (st screenIPBanStore) IsIPBanned(ip string) (bool, error) {
	st.factory.checked <- ip
	return st.factory.banned, st.factory.err
}
func (st screenIPBanStore) IsBootstrapUser(int64) (bool, error) { return st.factory.protected, nil }

type screenDeadlineProbe struct {
	net.Conn
	cleared chan struct{}
}

func (c screenDeadlineProbe) SetDeadline(at time.Time) error {
	if at.IsZero() {
		close(c.cleared)
	}
	return c.Conn.SetDeadline(at)
}
func TestScreenIngressPreservesBootstrapIPExemption(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "recovery", model.RoleAdmin)
	srv.store = screenIPBanFactory{banned: true, protected: true, checked: make(chan string, 1)}
	server, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	cleared := make(chan struct{})
	conn := screenDeadlineProbe{Conn: remoteAddrConn{Conn: server, remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.31"), Port: 1234}}, cleared: cleared}
	done := make(chan struct{})
	go func() { defer close(done); srv.handleScreenConn(conn) }()
	if err := protocol.WriteScreenAuth(peer, &protocol.ScreenAuth{SessionID: session.ID, Token: session.ScreenAuthToken}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleared:
	case <-time.After(time.Second):
		t.Fatal("bootstrap screen connection rejected")
	}
	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("screen handler did not stop")
	}
}

func TestScreenIngressRejectsBannedIPAndStoreFailure(t *testing.T) {
	for _, storeErr := range []error{nil, errors.New("database failed")} {
		srv, _, _ := newTestServer(t)
		session := mustCreateSession(t, srv.sessions, 1, "screen", model.RoleUser)
		checked := make(chan string, 1)
		srv.store = screenIPBanFactory{banned: true, err: storeErr, checked: checked}
		serverSide, clientSide := net.Pipe()
		defer clientSide.Close() //nolint:errcheck
		conn := &remoteAddrConn{Conn: serverSide, remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.31"), Port: 1234}}
		done := make(chan struct{})
		go func() { defer close(done); srv.handleScreenConn(conn) }()
		if err := protocol.WriteScreenAuth(clientSide, &protocol.ScreenAuth{SessionID: session.ID, Token: session.ScreenAuthToken}); err != nil {
			t.Fatal(err)
		}
		select {
		case ip := <-checked:
			if ip != "192.0.2.31" {
				t.Fatal(ip)
			}
		case <-time.After(time.Second):
			_ = clientSide.Close()
			t.Fatal("screen ingress did not check IP ban")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = clientSide.Close()
			t.Fatal("banned screen ingress waited for auth")
		}
	}
}
