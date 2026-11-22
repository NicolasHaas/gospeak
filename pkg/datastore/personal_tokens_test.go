package datastore_test

import (
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestPersonalTokenCASRejectsStaleReplacement(t *testing.T) {
	st, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.NonTx().CreateUser("cas-target", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	oldHash, firstHash, staleHash := crypto.HashToken("old"), crypto.HashToken("first"), crypto.HashToken("stale")
	if err := st.NonTx().UpdateUserPersonalToken(user.ID, oldHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	replace := func(expected, replacement string) bool {
		t.Helper()
		tx, err := st.Tx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		changed, err := tx.ReplacePersonalToken(user.ID, 1, expected, replacement, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !replace(oldHash, firstHash) {
		t.Fatal("current replacement refused")
	}
	if replace(oldHash, staleHash) {
		t.Fatal("stale replacement overwrote newer personal credential")
	}
	if user, err := st.NonTx().GetUserByPersonalTokenHash(firstHash); err != nil || user == nil {
		t.Fatal("winning credential lost")
	}
	if user, err := st.NonTx().GetUserByPersonalTokenHash(oldHash); err != nil || user != nil {
		t.Fatal("old credential still authenticates")
	}
}

func credentialTx(t *testing.T, st *datastore.ProviderFactory, change func(datastore.DataStoreTx) (bool, error)) bool {
	t.Helper()
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := change(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return changed
}

func personalRecoveryIssuer(t *testing.T, st *datastore.ProviderFactory) int64 {
	t.Helper()
	issuer, err := st.NonTx().CreateUser("recovery-issuer", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(issuer.ID, crypto.HashToken("issuer-personal"), time.Now()); err != nil {
		t.Fatal(err)
	}
	return issuer.ID
}

func TestPersonalRecoveryAccountBoundExpiringOneUse(t *testing.T) {
	st, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{Name: "recovery-scope"}
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	target, err := st.NonTx().CreateUserWithChannelScope("recovery-target", model.RoleModerator, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.NonTx().CreateUser("recovery-other", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0).UTC()
	issuerID := personalRecoveryIssuer(t, st)
	oldHash, grant, replacement := crypto.HashToken("old"), crypto.HashToken("grant"), crypto.HashToken("new")
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, oldHash, now); err != nil {
		t.Fatal(err)
	}
	issue := func(id, version int64, hash string) bool {
		return credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
			return tx.IssuePersonalRecovery(id, version, hash, now, issuerID)
		})
	}
	redeem := func(id int64, hash string, at time.Time) bool {
		return credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
			return tx.RedeemPersonalRecovery(id, hash, replacement, at)
		})
	}
	if !issue(target.ID, 1, grant) {
		t.Fatal("ordinary recovery issuance failed")
	}
	if user, err := st.NonTx().GetUserByPersonalTokenHash(oldHash); err != nil || user != nil {
		t.Fatal("issuance did not revoke old credential")
	}
	if redeem(other.ID, grant, now) {
		t.Fatal("wrong account consumed grant")
	}
	if redeem(target.ID, crypto.HashToken("wrong"), now) {
		t.Fatal("wrong bearer consumed grant")
	}
	if redeem(target.ID, grant, now.Add(datastore.PersonalRecoveryLifetime)) {
		t.Fatal("expired grant consumed")
	}
	if !redeem(target.ID, grant, now.Add(time.Minute)) {
		t.Fatal("valid grant refused")
	}
	if redeem(target.ID, grant, now) {
		t.Fatal("reused grant accepted")
	}
	if issue(target.ID, 1, crypto.HashToken("stale-grant")) {
		t.Fatal("stale admin retry revoked recovered credential")
	}
	got, err := st.NonTx().GetUserByPersonalTokenHash(replacement)
	if err != nil || got == nil || got.ID != target.ID || got.Username != target.Username || got.Role != target.Role || got.ChannelScope != target.ChannelScope {
		t.Fatal("recovery replaced identity or authority")
	}
	version, err := st.NonTx().PersonalTokenVersion(target.ID)
	if err != nil || version != 3 {
		t.Fatalf("credential version = %d, %v; want 3", version, err)
	}
}

var _ datastore.DataProviderFactory = (*datastore.ProviderFactory)(nil)

func TestPersonalTokenCollisionNoopAndRollbackPreserveIdentity(t *testing.T) {
	st, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.NonTx().CreateUser("rollback-target", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.NonTx().CreateUser("collision-target", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old, occupied := crypto.HashToken("old"), crypto.HashToken("occupied")
	issuerID := personalRecoveryIssuer(t, st)
	if err := st.NonTx().UpdateUserPersonalToken(u.ID, old, now); err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(other.ID, occupied, now); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{old, occupied} {
		if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
			return tx.ReplacePersonalToken(u.ID, 1, old, candidate, now)
		}) {
			t.Fatal("same or occupied token replaced identity")
		}
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := tx.ReplacePersonalToken(u.ID, 1, old, crypto.HashToken("uncommitted"), now)
	if err != nil || !changed {
		_ = tx.Rollback()
		t.Fatal("prepared replacement failed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := st.NonTx().GetUserByID(u.ID)
	version, versionErr := st.NonTx().PersonalTokenVersion(u.ID)
	if err != nil || versionErr != nil || version != 1 || got.PersonalTokenHash != old || got.ID != u.ID || got.Username != u.Username || got.Role != u.Role || got.ChannelScope != u.ChannelScope {
		t.Fatal("rollback changed identity or credential version")
	}
	if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.ReplacePersonalToken(99999, 1, old, crypto.HashToken("missing"), now)
	}) {
		t.Fatal("missing target reported success")
	}
	if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.IssuePersonalRecovery(u.ID, 1, "grant-collision", now, issuerID)
	}) {
		t.Fatal("issue failed")
	}
	if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.RedeemPersonalRecovery(u.ID, "grant-collision", occupied, now)
	}) {
		t.Fatal("recovery installed another account credential")
	}
	if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.RedeemPersonalRecovery(u.ID, "grant-collision", crypto.HashToken("unique-recovery"), now)
	}) {
		t.Fatal("collision consumed recovery grant")
	}
}

func TestPersonalRecoveryReissueRevocationAndBan(t *testing.T) {
	for _, action := range []string{"reissue", "revoke", "rotate", "ban", "promote"} {
		t.Run(action, func(t *testing.T) {
			st, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatal(err)
			}
			u, err := st.NonTx().CreateUser("recovery-invalidation", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1800000000, 0)
			issuerID := personalRecoveryIssuer(t, st)
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(u.ID, 0, "grant-one", now, issuerID)
			}) {
				t.Fatal("issue failed")
			}
			switch action {
			case "reissue":
				if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
					return tx.IssuePersonalRecovery(u.ID, 1, "grant-two", now, issuerID)
				}) {
					t.Fatal("reissue failed")
				}
			case "revoke", "rotate":
				candidate := ""
				if action == "rotate" {
					candidate = crypto.HashToken("local-candidate")
				}
				if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
					return tx.ReplacePersonalToken(u.ID, 1, "", candidate, now)
				}) {
					t.Fatal("replacement failed")
				}
			case "ban":
				if err := st.NonTx().CreateUserBan(u.ID, 0, time.Time{}); err != nil {
					t.Fatal(err)
				}
			case "promote":
				if err := st.NonTx().UpdateUserRole(u.ID, model.RoleAdmin); err != nil {
					t.Fatal(err)
				}
			}
			if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.RedeemPersonalRecovery(u.ID, "grant-one", crypto.HashToken("unauthorized"), now)
			}) {
				t.Fatal("invalidated grant redeemed")
			}
		})
	}
}

func TestPersonalRecoveryNeverTargetsAdministratorOrBootstrapOwner(t *testing.T) {
	st, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.NonTx().CreateUser("recovery-admin", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.IssuePersonalRecovery(admin.ID, 0, "grant", now, admin.ID)
	}) {
		t.Fatal("administrator received remote recovery")
	}
	if err := st.NonTx().CreateBootstrapToken("setup-hash"); err != nil {
		t.Fatal(err)
	}
	var owner *model.User
	credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		var err error
		owner, err = tx.ProvisionBootstrapUser("setup-hash", "recovery-owner", crypto.HashToken("owner-personal"), now)
		return err == nil, err
	})
	// Durable ownership, not merely current role, protects the marker.
	if err := st.NonTx().UpdateUserRole(owner.ID, model.RoleUser); err != nil {
		t.Fatal(err)
	}
	if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.IssuePersonalRecovery(owner.ID, 1, "grant", now, admin.ID)
	}) {
		t.Fatal("bootstrap owner received remote recovery")
	}
}

func TestPersonalTokenCASConcurrentWriterOrders(t *testing.T) {
	for _, first := range []string{"rotate", "revoke"} {
		t.Run(first, func(t *testing.T) {
			st, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatal(err)
			}
			u, err := st.NonTx().CreateUser("concurrent-credential", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			old := crypto.HashToken("old")
			if err := st.NonTx().UpdateUserPersonalToken(u.ID, old, time.Now()); err != nil {
				t.Fatal(err)
			}
			tx, err := st.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			firstHash, secondHash := crypto.HashToken("candidate"), ""
			if first == "revoke" {
				firstHash, secondHash = secondHash, firstHash
			}
			changed, err := tx.ReplacePersonalToken(u.ID, 1, old, firstHash, time.Now())
			if err != nil || !changed {
				t.Fatal("first writer failed")
			}
			type result struct {
				changed bool
				err     error
			}
			finished := make(chan result, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				second, err := st.Tx(context.Background())
				if err != nil {
					finished <- result{false, err}
					return
				}
				defer func() { _ = second.Rollback() }()
				changed, err := second.ReplacePersonalToken(u.ID, 1, old, secondHash, time.Now())
				if err == nil {
					err = second.Commit()
				}
				finished <- result{changed, err}
			}()
			<-started
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-finished:
				if got.err != nil || got.changed {
					t.Fatal("concurrent stale writer replaced credential")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("second writer did not finish")
			}
			got, err := st.NonTx().GetUserByID(u.ID)
			if err != nil || got.PersonalTokenHash != firstHash {
				t.Fatal("wrong credential won")
			}
		})
	}
}
