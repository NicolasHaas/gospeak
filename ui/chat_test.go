package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatHistoryMergesLivePagesAndDeletion(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.chatChannelID = 2
	a.chatDeleted = make(map[int64]bool)
	a.chatLoading = true
	a.addChatMessage(pb.ChatMessage{ID: 3, ChannelID: 2, Text: "live"})
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 1, Messages: []pb.ChatMessage{{ID: 99, ChannelID: 1}}})
	if !a.chatLoading || len(a.chatRows) != 1 {
		t.Fatal("another channel's history changed the selected chat")
	}
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2, HasMore: true, Messages: []pb.ChatMessage{
		{ID: 3, ChannelID: 2, Text: "live"},
		{ID: 2, ChannelID: 2, Text: "middle"},
	}})
	if a.chatLoading || !a.chatHasMore || a.chatBeforeID != 2 || !a.chatMore.Visible() || len(a.chatRows) != 2 || a.chatRows[0].ID != 2 || a.chatRows[1].ID != 3 {
		t.Fatalf("initial page and live overlap: %+v", a.chatRows)
	}
	a.chatLoading = true
	a.removeChatMessage(pb.ChatDeleteEvent{ChannelID: 2, MessageID: 1})
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2, Messages: []pb.ChatMessage{
		{ID: 2, ChannelID: 2}, {ID: 1, ChannelID: 2},
	}})
	if len(a.chatRows) != 2 || a.chatBeforeID != 1 || a.chatRows[0].ID != 2 || a.chatRows[1].ID != 3 || a.chatMore.Visible() {
		t.Fatalf("older page duplicated or resurrected deleted row: %+v", a.chatRows)
	}
	a.removeChatMessage(pb.ChatDeleteEvent{ChannelID: 2, MessageID: 3})
	if len(a.chatRows) != 1 || a.chatRows[0].ID != 2 || len(a.chatBox.Objects) != 1 {
		t.Fatalf("deletion was not reflected: %+v", a.chatRows)
	}
	a.resetChat()
	if a.chatChannelID != 0 || len(a.chatRows) != 0 || len(a.chatBox.Objects) != 0 || !a.chatEntry.Disabled() {
		t.Fatal("disconnect did not clear chat")
	}
}

func TestRemovedChannelClearsTextSelection(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	a.channels = []pb.ChannelInfo{{ID: 1, Name: "Lobby"}, {ID: 2, Name: "Games"}}
	a.selectedChannelID = 2
	a.chatChannelID = 2
	a.chatRows = []pb.ChatMessage{{ID: 4, ChannelID: 2}}
	a.renderChat()
	a.engine.OnChannelsUpdate([]pb.ChannelInfo{{ID: 1, Name: "Lobby"}})
	fyne.DoAndWait(func() {})
	if a.selectedChannelID != 1 || a.chatChannelID != 0 || len(a.chatRows) != 0 || !a.chatEntry.Disabled() {
		t.Fatal("removed channel left stale text selection")
	}
	a.chatChannelID = 1
	a.engine.OnChannelsUpdate(nil)
	fyne.DoAndWait(func() {})
	if a.selectedChannelID != 0 || a.chatChannelID != 0 || !a.chatEntry.Disabled() {
		t.Fatal("empty channel list left stale chat enabled")
	}
}
