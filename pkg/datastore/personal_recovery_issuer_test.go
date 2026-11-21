package datastore_test

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestPersonalRecoveryRechecksIssuerAndGrantRevocation(t *testing.T) {
	for _, change := range []string{"demote", "ban", "credential", "revoke-grant"} {
		t.Run(change, func(t *testing.T) {
			st, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatal(err)
			}
			issuer := personalRecoveryIssuer(t, st)
			target, err := st.NonTx().CreateUser("target", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(target.ID, 0, "grant", now, issuer)
			}) {
				t.Fatal("grant not issued")
			}
			switch change {
			case "demote":
				err = st.NonTx().UpdateUserRole(issuer, model.RoleUser)
			case "ban":
				err = st.NonTx().CreateUserBan(issuer, 0, time.Time{})
			case "credential":
				err = st.NonTx().UpdateUserPersonalToken(issuer, "", now)
			case "revoke-grant":
				if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) { return tx.RevokePersonalRecovery(target.ID, 1) }) {
					t.Fatal("grant revocation failed")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.RedeemPersonalRecovery(target.ID, "grant", crypto.HashToken("candidate"), now)
			}) {
				t.Fatal("stale grant retained recovery authority")
			}
		})
	}
}
