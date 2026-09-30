package datastore_test

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestMessageStorageRejectsInvalidText(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	ch := &model.Channel{Name: "hygiene"}
	if err := store.NonTx().CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"bad\xff", "escape\x1b", "bidi\u202e", "hidden\u200b"} {
		m := &model.Message{ChannelID: ch.ID, SenderID: 1, SenderName: "sender", Body: body}
		if err := store.NonTx().CreateMessage(m); err == nil {
			t.Errorf("stored invalid body %q", body)
		}
	}
	rows, err := store.NonTx().ListMessages(model.MessageFilters{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
