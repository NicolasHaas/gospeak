package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// Hold exactly the reviewer's boundary: local close completed, but normal
// cleanup has not detached the account slot. No authentication or CAS is mocked.
func TestPersonalCredentialAdmissionAtHeldCleanup(t *testing.T) {
	for _, mode := range []string{"inspection", "engine", "held", "released"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			srv, st, handler := newTestServerWithConfig(t, func(c *Config) { c.MaxSessionsPerUser = 2; c.EnableScreenShare = false })
			actor, err := st.NonTx().CreateUser("actor", model.RoleAdmin)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.NonTx().UpdateUserPersonalToken(actor.ID, crypto.HashToken("personal"), time.Now()); err != nil {
				t.Fatal(err)
			}
			target, err := st.NonTx().CreateUser("target", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("target-personal"), time.Now()); err != nil {
				t.Fatal(err)
			}
			var selected atomic.Uint32
			gate, entered, refused := make(chan struct{}), make(chan struct{}), make(chan struct{}, 32)
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			srv.beforeControlCleanup = func(id uint32) {
				if id == selected.Load() {
					close(entered)
					<-gate
				}
			}
			srv.sessionAdmissionRefused = func() { refused <- struct{}{} }
			addr, pin := personalCredentialListener(t, srv, handler, st)
			defer release()
			other, _, err := client.OpenPersonalTokenControl(t.Context(), addr, pin, actor.Username, "personal")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			var version int64
			if mode == "engine" {
				alsa := filepath.Join(t.TempDir(), "alsa.conf")
				if err := os.WriteFile(alsa, []byte("pcm.!default { type null }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ALSA_CONFIG_PATH", alsa)
				t.Setenv("JACK_NO_START_SERVER", "1")
				engine := client.NewEngine()
				defer engine.Disconnect()
				if err := engine.ConnectWithAutoToken(addr, "127.0.0.1:9", "personal", actor.Username, pin, nil); err != nil {
					t.Fatal(err)
				}
				selected.Store(engine.GetSessionID())
				engine.Disconnect() // local workers joined, NOT server retirement
				if engine.GetState() != client.StateDisconnected {
					t.Fatal("engine did not finish local disconnect")
				}
				version, err = st.NonTx().PersonalTokenVersion(target.ID)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				inspect, auth, err := client.OpenPersonalTokenControl(t.Context(), addr, pin, actor.Username, "personal")
				if err != nil {
					t.Fatal(err)
				}
				selected.Store(auth.SessionID)
				response, err := inspect.PersonalTokenExchange(t.Context(), &pb.ControlMessage{PersonalTokenStatusReq: &pb.PersonalTokenStatusRequest{TargetID: target.ID}})
				if err != nil {
					t.Fatal(err)
				}
				version = response.PersonalTokenStatusResp.Version
				if err := inspect.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("server cleanup boundary not reached")
			}
			if len(srv.sessions.GetAllByUserIDSnapshots(actor.ID)) != 2 {
				t.Fatal("local close incorrectly treated as server retirement")
			}
			if mode == "released" {
				release()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				c, _, err := client.OpenPersonalTokenControl(ctx, addr, pin, actor.Username, "personal")
				if err == nil {
					defer c.Close()
					_, err = c.PersonalTokenExchange(ctx, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}})
				}
				done <- err
			}()
			if mode != "released" {
				select {
				case <-refused:
				case <-time.After(4 * time.Second):
					t.Fatal("real capacity refusal not observed")
				}
				got, err := st.NonTx().GetUserByID(target.ID)
				if err != nil || got.PersonalTokenHash == "" {
					t.Fatal("mutation ran before admission")
				}
				if mode != "held" {
					release()
				}
			}
			err = <-done
			got, readErr := st.NonTx().GetUserByID(target.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "held" {
				if !client.PersonalCredentialAdmissionRefused(err) || client.PersonalCredentialRejected(err) || got.PersonalTokenHash == "" {
					t.Fatalf("held admission must fail before mutation: %T %v", err, err)
				}
			} else if err != nil || got.PersonalTokenHash != "" {
				t.Fatalf("mutation failed after server retirement: %T %v", err, err)
			}
			if got.ID != target.ID || got.Role != target.Role || got.ChannelScope != target.ChannelScope {
				t.Fatal("account authority changed")
			}
		})
	}
}
