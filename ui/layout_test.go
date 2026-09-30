package ui

import (
	"errors"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestMainWindowFitsDefaultWidth(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	checkWidth := func() {
		t.Helper()
		a.window.Resize(fyne.NewSize(800, 600))
		a.window.Content().Refresh()
		if width := a.window.Content().MinSize().Width; width > 800 {
			t.Fatalf("window requires %.0f px", width)
		}
		if a.mainArea.Visible() && (a.chatHeader.Size().Width < 200 || a.voiceStatus.Size().Width < 200) {
			t.Fatal("chat or voice header collapsed")
		}
	}
	if !a.welcome.Visible() || a.mainArea.Visible() || a.mediaControls.Visible() || a.vadIndicator.Visible() || a.disconnectBtn.Visible() {
		t.Fatal("disconnected view must show Connect, not inactive channels or media controls")
	}
	checkWidth()
	a.engine.OnStateChange(client.StateConnected)
	fyne.DoAndWait(func() {})
	if a.welcome.Visible() || !a.mainArea.Visible() || !a.mediaControls.Visible() || !a.vadIndicator.Visible() || !a.disconnectBtn.Visible() || a.shareBtn.Visible() {
		t.Fatal("connected view must show channels and audio controls but hide unavailable sharing")
	}
	checkWidth()
	a.engine.OnAudioFailure(errors.New("no input device"))
	fyne.DoAndWait(func() {})
	a.serverBtn.Show()
	a.shareBtn.Show()
	a.shareBtn.SetText("Stop Sharing")
	a.shareChannelBtn.Show()
	a.showScreenPanel()
	checkWidth()
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if !a.welcome.Visible() || a.mainArea.Visible() || a.mediaControls.Visible() || a.vadIndicator.Visible() || a.disconnectBtn.Visible() {
		t.Fatal("disconnect must restore the connection view")
	}

	checkWidth()
}

func TestChatDraftSurvivesSendErrorAndJoinRequest(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	a.chatEntry.SetText("unsent text")
	a.chatEntry.OnSubmitted(a.chatEntry.Text) // disconnected: SendChat fails
	if a.chatEntry.Text != "unsent text" {
		t.Fatal("failed send discarded the draft")
	}
	a.chatBox.Add(widget.NewLabel("earlier message"))
	a.selectedChannelID = 2
	a.joinSelectedChannel() // disconnected: no request was sent
	if len(a.chatBox.Objects) != 1 {
		t.Fatal("failed join cleared chat")
	}
	a.engine.OnChannelJoined(2)
	fyne.DoAndWait(func() {})
	if len(a.chatBox.Objects) != 1 {
		t.Fatal("voice join cleared independent text chat")
	}
	a.chatBox.Add(widget.NewLabel("server A message"))
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if len(a.chatBox.Objects) != 0 {
		t.Fatal("disconnect left old server chat visible")
	}
}

func TestChatContextFollowsTextSelectionNotVoice(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.channels = []pb.ChannelInfo{{ID: 1, Name: "Lobby"}, {ID: 2, Name: "Games"}}
	a.chatChannelID = 2
	a.updateChatContext()
	if a.chatHeader.Text != "Chat: Games" || a.chatEntry.Disabled() {
		t.Fatal("Games text chat must work without voice")
	}
	a.chatChannelID = 1
	a.updateChatContext()
	if a.chatHeader.Text != "Chat: Lobby" || a.chatEntry.Disabled() {
		t.Fatal("text channel must be independent of voice")
	}
	a.chatChannelID = 0
	a.updateChatContext()
	if a.chatHeader.Text != "Select a text channel" || !a.chatEntry.Disabled() {
		t.Fatal("chat must be unavailable without a text selection")
	}
	a.channels[0].Name = strings.Repeat("W", 64)
	a.chatChannelID = 1
	a.updateChatContext()
	if width := a.window.Content().MinSize().Width; width > 800 {
		t.Fatalf("long channel name makes window %.0f px wide", width)
	}
}
