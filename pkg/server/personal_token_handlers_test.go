package server

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestPersonalTokenRotationWireRetiresOldCredential(t *testing.T) {
	srv, st, handler := newTestServer(t)
	user, err := st.NonTx().CreateUser("wire-rotation", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken("historical-personal"), time.Now()); err != nil {
		t.Fatal(err)
	}
	c := connectPublicationClient(t, srv, handler, st)
	auth, err := c.Authenticate("historical-personal", user.Username)
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan *pb.ControlMessage, 16)
	c.SetEventHandler(func(m *pb.ControlMessage) { responses <- m })
	c.StartReceiving()
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(&pb.ControlMessage{RotatePersonalTokenReq: &pb.RotatePersonalTokenRequest{Version: auth.CredentialVersion, CurrentToken: "historical-personal", Replacement: candidate}}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-responses:
			if m.RotatePersonalTokenResp == nil {
				continue
			}
			if m.RotatePersonalTokenResp.Error != "" {
				t.Fatalf("rotation refused: %s", m.RotatePersonalTokenResp.Error)
			}
			goto completed
		case <-timer.C:
			t.Fatal("rotation request produced no completion")
		}
	}
completed:
	if sessions := srv.sessions.GetAllByUserIDSnapshots(user.ID); len(sessions) != 0 {
		t.Fatal("rotation completion retained target authority")
	}
	old := connectPublicationClient(t, srv, handler, st)
	if _, err := old.Authenticate("historical-personal", user.Username); err == nil {
		t.Fatal("old credential authenticated")
	}
	next := connectPublicationClient(t, srv, handler, st)
	got, err := next.Authenticate(candidate, user.Username)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != user.ID || got.Username != user.Username || got.Role != "moderator" || got.CredentialVersion != auth.CredentialVersion+1 {
		t.Fatal("rotation changed identity or authority")
	}
}
