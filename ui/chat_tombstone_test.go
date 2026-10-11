package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// A hostile server spamming delete events must not grow the tombstone map
// without bound while the user stays parked on one channel.
func TestChatTombstonesAreBounded(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.chatChannelID = 2
	a.chatDeleted = make(map[int64]bool)

	for id := int64(1); id <= 5000; id++ {
		a.removeChatMessage(pb.ChatDeleteEvent{ChannelID: 2, MessageID: id})
	}
	if got := len(a.chatDeleted); got > 1000 {
		t.Fatalf("tombstone map grew to %d entries, want at most 1000", got)
	}
	// Recent deletions still work after the cap: the newest event still
	// removes a visible row.
	a.chatRows = []pb.ChatMessage{{ID: 6000, ChannelID: 2, Text: "latest"}}
	a.removeChatMessage(pb.ChatDeleteEvent{ChannelID: 2, MessageID: 6000})
	if len(a.chatRows) != 0 {
		t.Fatalf("newest deletion after cap did not remove the row: %+v", a.chatRows)
	}
}
