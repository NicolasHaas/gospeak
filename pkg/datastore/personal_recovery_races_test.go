package datastore_test

import (
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestPersonalRecoveryConcurrentIssueRedeemOrders(t *testing.T) {
	for _, first := range []string{"issue", "redeem"} {
		t.Run(first, func(t *testing.T) {
			st, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatal(err)
			}
			u, err := st.NonTx().CreateUser("competing-recovery", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			issuerID := personalRecoveryIssuer(t, st)
			candidate := crypto.HashToken("candidate")
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(u.ID, 0, "initial-grant", now, issuerID)
			}) {
				t.Fatal("initial issuance failed")
			}
			issue := func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(u.ID, 1, "new-grant", now, issuerID)
			}
			redeem := func(tx datastore.DataStoreTx) (bool, error) {
				return tx.RedeemPersonalRecovery(u.ID, "initial-grant", candidate, now)
			}
			winner, loser := issue, redeem
			if first == "redeem" {
				winner, loser = redeem, issue
			}
			firstTx, err := st.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = firstTx.Rollback() }()
			changed, err := winner(firstTx)
			if err != nil || !changed {
				t.Fatal("first operation failed")
			}
			type result struct {
				changed bool
				err     error
			}
			started := make(chan struct{})
			done := make(chan result, 1)
			go func() {
				close(started)
				tx, err := st.Tx(context.Background())
				if err != nil {
					done <- result{false, err}
					return
				}
				defer func() { _ = tx.Rollback() }()
				changed, err := loser(tx)
				if err == nil {
					err = tx.Commit()
				}
				done <- result{changed, err}
			}()
			<-started
			if err := firstTx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.err != nil || got.changed {
					t.Fatal("stale competing operation changed recovered state")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("competing writer did not join")
			}
			got, err := st.NonTx().GetUserByID(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if first == "redeem" && got.PersonalTokenHash != candidate {
				t.Fatal("ambiguous stale issuance revoked recovered credential")
			}
			if first == "issue" {
				if got.PersonalTokenHash != "" {
					t.Fatal("issuance retained personal credential")
				}
				if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
					return tx.RedeemPersonalRecovery(u.ID, "new-grant", candidate, now)
				}) {
					t.Fatal("winning reissued grant not redeemable")
				}
			}
		})
	}
}
