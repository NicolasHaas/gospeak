package datastore_test

import (
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestPruneExpiredBansBoundedAndPermanent(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	// SQLite's own clock fixes the equality boundary without a wall-clock race.
	if _, err := store.DB.ExecContext(context.Background(), `INSERT INTO bans(user_id,expires_at) VALUES
 (1,datetime('now','-1 day')), (1,datetime('now')), (1,datetime('now','+1 day')), (1,NULL)`); err != nil {
		t.Fatal(err)
	}
	st := store.NonTx()
	for _, limit := range []int{0, -1, datastore.MaxBanPageSize + 1} {
		if _, err := st.PruneExpiredBans(limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	for i := 0; i < 2; i++ {
		if n, err := st.PruneExpiredBans(1); err != nil || n != 1 {
			t.Fatalf("prune=%d %v", n, err)
		}
	}
	if n, err := st.PruneExpiredBans(1); err != nil || n != 0 {
		t.Fatalf("extra prune=%d %v", n, err)
	}
	bans, _, err := st.ListActiveBans(0, 100)
	if err != nil || len(bans) != 2 {
		t.Fatalf("active=%v %v", bans, err)
	}
	if !bans[1].ExpiresAt.IsZero() || !bans[0].ExpiresAt.After(time.Now()) {
		t.Fatal("permanent/future ban changed")
	}
}
