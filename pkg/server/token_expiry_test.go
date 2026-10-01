package server

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestTokenRequestRejectsInvalidExpiry(t *testing.T) {
	for _, seconds := range []int64{-1, 10*365*86400 + 1, math.MaxInt64} {
		srv, st, _ := newTestServer(t)
		actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
		conn := &bufferConn{}
		srv.handleCreateToken(actor.ID, &pb.CreateTokenRequest{Role: "user", ExpiresInSeconds: seconds}, st, conn)
		response, err := protocol.ReadControlMessage(conn)
		if err != nil || response.ErrorResponse == nil {
			t.Errorf("invalid expiry %d accepted", seconds)
		}
		if srv.metrics.TokensCreated.Load() != 0 {
			t.Error("invalid request minted token")
		}
	}
}
func TestTokenExpiryBoundaries(t *testing.T) {
	for _, seconds := range []int64{0, 1, 10 * 365 * 86400} {
		srv, st, _ := newTestServer(t)
		actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
		conn := &bufferConn{}
		before := time.Now().Add(time.Duration(seconds) * time.Second)
		srv.handleCreateToken(actor.ID, &pb.CreateTokenRequest{Role: "user", ExpiresInSeconds: seconds}, st, conn)
		response, err := protocol.ReadControlMessage(conn)
		if err != nil || response.CreateTokenResp == nil {
			t.Fatalf("boundary %d rejected: %#v %v", seconds, response, err)
		}
		var maxUses int
		var expires sql.NullInt64
		err = st.(*datastore.ProviderFactory).DB.QueryRowContext(context.Background(), "SELECT max_uses, strftime('%s', expires_at) FROM tokens WHERE hash = ?", crypto.HashToken(response.CreateTokenResp.Token)).Scan(&maxUses, &expires)
		if err != nil {
			t.Fatal(err)
		}
		if maxUses != 0 {
			t.Fatal("zero uses must be unlimited")
		}
		if seconds == 0 {
			if expires.Valid {
				t.Fatal("zero expiry must be NULL")
			}
		} else if !expires.Valid || expires.Int64 < before.Unix() || expires.Int64 > time.Now().Add(time.Duration(seconds)*time.Second).Unix() {
			t.Fatal("wrong persisted expiry")
		}

	}
}
