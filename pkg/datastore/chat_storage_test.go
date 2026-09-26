package datastore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestChatStorageCursorAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	store, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NonTx()
	if err := st.CreateMessageWithRetention(nil, 3, 0); err == nil {
		t.Fatal("nil message accepted")
	}
	tx, err := store.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.CreateMessageWithRetention(nil, 3, 0); err == nil {
		t.Fatal("nil transactional message accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{Name: "chat"}
	if err := st.CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"界 one", "two", "three", "four"} {
		m := &model.Message{ChannelID: channel.ID, SenderID: 7, SenderName: "former-user", Body: body}
		if err := st.CreateMessageWithRetention(m, 3, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st = store.NonTx()
	size := int64(2)
	channelID := channel.ID
	first, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channelID, PageSize: &size})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Body != "four" || first[1].Body != "three" || first[0].SenderName != "former-user" {
		t.Fatalf("first page: %+v", first)
	}
	if first[0].CreatedAt.IsZero() {
		t.Fatal("stored timestamp missing")
	}
	newer, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channelID, Since: first[0].CreatedAt.Add(time.Nanosecond)})
	if err != nil || len(newer) != 0 {
		t.Fatalf("sub-second cursor included older message: %v, %v", newer, err)
	}
	if err := st.CreateMessageWithRetention(&model.Message{ChannelID: channelID, SenderID: 7, SenderName: "former-user", Body: "later"}, 4, 0); err != nil {
		t.Fatal(err)
	}
	second, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channelID, PageSize: &size, BeforeID: first[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Body != "two" {
		t.Fatalf("second page: %+v", second)
	}
	if _, err := st.ListMessages(model.MessageFilters{PageSize: ptr(int64(-1))}); err == nil {
		t.Fatal("negative page size accepted")
	}
	if _, err := st.ListMessages(model.MessageFilters{PageSize: ptr(int64(1000000))}); err == nil {
		t.Fatal("unbounded page accepted")
	}
	for _, bad := range []*model.Message{
		{ChannelID: -1, SenderID: 7, SenderName: "former-user", Body: "bad channel"},
		{ChannelID: channelID, SenderID: 7, SenderName: "", Body: "missing snapshot"},
		{ChannelID: channelID, SenderID: 7, SenderName: strings.Repeat("x", 33), Body: "bad sender"},
	} {
		if err := st.CreateMessageWithRetention(bad, 3, 0); err == nil {
			t.Fatalf("accepted invalid message: %+v", bad)
		}
	}
	if err := st.DeleteChannel(channel.ID); err != nil {
		t.Fatal(err)
	}
	leftover, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channelID})
	if err != nil || len(leftover) != 0 {
		t.Fatalf("deleted channel history: %v, %v", leftover, err)
	}
	if err := st.CreateMessageWithRetention(&model.Message{ChannelID: channelID, SenderID: 7, SenderName: "former-user", Body: "orphan"}, 3, 0); err == nil {
		t.Fatal("stored history for deleted channel")
	}
}

func TestChatRetentionAgeAndTransaction(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NonTx()
	createMessageChannel(t, st, "age-chat")
	if err := st.CreateMessageWithRetention(&model.Message{ChannelID: 1, SenderID: 2, SenderName: "old", Body: "tiny"}, 10, time.Millisecond); err == nil {
		t.Fatal("sub-second retention accepted with second-precision timestamps")
	}
	old := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "old", Body: "old"}
	if err := st.CreateMessageWithRetention(old, 10, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(context.Background(), "UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE id = ?", old.ID); err != nil {
		t.Fatal(err)
	}
	newer := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "new", Body: "new"}
	if err := st.CreateMessageWithRetention(newer, 10, time.Hour); err != nil {
		t.Fatal(err)
	}
	id := int64(1)
	got, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &id})
	if err != nil || len(got) != 1 || got[0].Body != "new" {
		t.Fatalf("age retention: %v, %v", got, err)
	}
	if !got[0].CreatedAt.Equal(newer.CreatedAt) {
		t.Fatalf("returned timestamp %v differs from persisted %v", newer.CreatedAt, got[0].CreatedAt)
	}
	if _, err := store.DB.ExecContext(context.Background(), `CREATE TRIGGER reject_message_prune BEFORE DELETE ON messages
		BEGIN SELECT RAISE(ABORT, 'prune rejected'); END`); err != nil {
		t.Fatal(err)
	}
	failed := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "new", Body: "must roll back"}
	if err := st.CreateMessageWithRetention(failed, 1, 0); err == nil {
		t.Fatal("prune failure did not roll back")
	}
	if failed.ID != 0 {
		t.Fatalf("failed insert published id %d", failed.ID)
	}
	got, err = st.ListMessages(model.MessageFilters{LimitToChannelID: &id})
	if err != nil || len(got) != 1 || got[0].Body != "new" {
		t.Fatalf("failed retention left partial message: %v, %v", got, err)
	}
	tx, err := store.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedTx := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "new", Body: "must roll back in tx"}
	if err := tx.CreateMessageWithRetention(failedTx, 1, 0); err == nil {
		t.Fatal("transactional prune unexpectedly succeeded")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListMessages(model.MessageFilters{LimitToChannelID: &id})
	if err != nil || len(got) != 1 || got[0].Body != "new" || failedTx.ID != 0 {
		t.Fatalf("transactional prune failure persisted partial message: %v, id=%d, err=%v", got, failedTx.ID, err)
	}
}

func ptr(v int64) *int64 { return &v }

func createMessageChannel(t *testing.T, st datastore.DataStore, name string) int64 {
	t.Helper()
	channel := &model.Channel{Name: name}
	if err := st.CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	return channel.ID
}

func TestChatStorageAcceptsCurrentChatLimit(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	createMessageChannel(t, store.NonTx(), "body-chat")
	body := strings.Repeat("界", 2000)
	m := &model.Message{ChannelID: 1, SenderID: 1, SenderName: "alice", Body: body}
	if err := store.NonTx().CreateMessageWithRetention(m, 500, 0); err != nil {
		t.Fatal(err)
	}
	if m.ID == 0 {
		t.Fatal("stored message has no ID")
	}
	m.Body += "界"
	if err := store.NonTx().CreateMessageWithRetention(m, 500, 0); err == nil {
		t.Fatal("accepted message beyond current chat limit")
	}
}

func TestChatStorageIdleExpirySweep(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NonTx()
	createMessageChannel(t, st, "idle-chat")
	for i := 0; i < 3; i++ {
		m := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "alice", Body: "old"}
		if err := st.CreateMessageWithRetention(m, 10, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.ExecContext(context.Background(), "UPDATE messages SET created_at = '2020-01-01 00:00:00'"); err != nil {
		t.Fatal(err)
	}
	channel := int64(1)
	got, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channel, Since: time.Now().Add(-time.Hour)})
	if err != nil || len(got) != 0 {
		t.Fatalf("expired history visible: %v, %v", got, err)
	}
	for _, want := range []int64{2, 1, 0} {
		deleted, err := st.PruneExpiredMessages(time.Hour, 2)
		if err != nil || deleted != want {
			t.Fatalf("sweep removed %d, want %d: %v", deleted, want, err)
		}
	}
	got, err = st.ListMessages(model.MessageFilters{LimitToChannelID: &channel})
	if err != nil || len(got) != 0 {
		t.Fatalf("idle history retained: %v, %v", got, err)
	}
}

func TestChatStoragePrunesLegacyBacklogInBatches(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NonTx()
	createMessageChannel(t, st, "backlog")
	_, err = store.DB.ExecContext(context.Background(), `WITH RECURSIVE n(x) AS (
		VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 1200)
		INSERT INTO messages (channel_id, sender_id, sender_name, body)
		SELECT 1, 2, 'alice', 'old' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := store.DB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, want := range []int{201, 3} {
		m := &model.Message{ChannelID: 1, SenderID: 2, SenderName: "alice", Body: "new"}
		if err := st.CreateMessageWithRetention(m, 3, 0); err != nil {
			t.Fatal(err)
		}
		if got := count(); got != want {
			t.Fatalf("remaining messages = %d, want %d", got, want)
		}
	}
}

func TestChatStorageMigratesExistingMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-chat.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(context.Background(), `
		CREATE TABLE schema_migrations (version INTEGER NOT NULL);
		INSERT INTO schema_migrations VALUES (10);
		CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE messages (id INTEGER PRIMARY KEY, channel_id INTEGER NOT NULL,
			sender_id INTEGER NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO channels VALUES (1, 'legacy');
		INSERT INTO messages VALUES (4, 1, 7, '界 legacy', '2020-01-02 03:04:05');
		INSERT INTO messages VALUES (5, 99, 7, 'orphan', '2020-01-02 03:04:05');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channelID := int64(1)
	got, err := store.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channelID})
	if err != nil || len(got) != 1 || got[0].ID != 4 || got[0].Body != "界 legacy" || got[0].SenderName != "" {
		t.Fatalf("legacy history: %v, %v", got, err)
	}
	all, err := store.NonTx().ListMessages(model.MessageFilters{})
	if err != nil || len(all) != 1 {
		t.Fatalf("orphaned legacy history survived migration: %v, %v", all, err)
	}
	cutoff := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if atCutoff, err := store.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channelID, Since: cutoff}); err != nil || len(atCutoff) != 1 {
		t.Fatalf("legacy timestamp excluded at exact cutoff: %v, %v", atCutoff, err)
	}
}
