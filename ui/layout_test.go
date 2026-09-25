package ui

import (
	"errors"
	"image/png"
	"os"
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
	if !a.welcome.Visible() || a.mainArea.Visible() || a.mediaControls.Visible() || a.vadIndicator.Visible() || a.disconnectBtn.Visible() {
		t.Fatal("disconnected view must show Connect, not inactive channels or media controls")
	}
	a.engine.OnStateChange(client.StateConnected)
	fyne.DoAndWait(func() {})
	if a.welcome.Visible() || !a.mainArea.Visible() || !a.mediaControls.Visible() || !a.vadIndicator.Visible() || !a.disconnectBtn.Visible() || a.shareBtn.Visible() {
		t.Fatal("connected view must show channels and audio controls but hide unavailable sharing")
	}
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if !a.welcome.Visible() || a.mainArea.Visible() || a.mediaControls.Visible() || a.vadIndicator.Visible() || a.disconnectBtn.Visible() {
		t.Fatal("disconnect must restore the connection view")
	}
	if os.Getenv("GOSPEAK_UI_SCREENSHOT") != "" {
		a.window.Resize(fyne.NewSize(800, 600))
		file, err := os.CreateTemp("", "gospeak-ui-*.png")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, a.window.Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
		t.Logf("screenshot: %s", file.Name())
	}
	for _, visible := range []bool{false, true} {
		if visible {
			a.mainArea.Show()
			a.welcome.Hide()
			a.disconnectBtn.Show()
			a.mediaControls.Show()
			a.vadIndicator.Show()
			a.shareBtn.Show()
			a.serverBtn.Show()
			a.shareChannelBtn.Show()
		}
		a.window.Content().Refresh()
		if width := a.window.Content().MinSize().Width; width > 800 {
			t.Fatalf("main window requires %.0f px (extra controls visible: %t), default window is 800 px", width, visible)
		}
	}
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
	if len(a.chatBox.Objects) != 0 {
		t.Fatal("accepted join did not clear old channel chat")
	}
	a.chatBox.Add(widget.NewLabel("server A message"))
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if len(a.chatBox.Objects) != 0 {
		t.Fatal("disconnect left old server chat visible")
	}
}

func TestAudioFailureIsVisibleAndResetsOnDisconnect(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	a.engine.OnStateChange(client.StateConnected)
	fyne.DoAndWait(func() {})
	a.engine.OnAudioFailure(errors.New("no input device"))
	fyne.DoAndWait(func() {})
	if got := a.vadIndicator.Text; got != "Audio unavailable" {
		t.Fatalf("audio status = %q, want unavailable", got)
	}
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if got := a.vadIndicator.Text; got != "Voice idle" {
		t.Fatalf("audio status after disconnect = %q, want reset", got)
	}
}

func TestChatFollowsVoiceUntilIndependentChatExists(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.channels = []pb.ChannelInfo{{ID: 1, Name: "Lobby"}, {ID: 2, Name: "Games"}}
	a.selectedChannelID = 2
	a.updateChatContext(1)
	if a.chatHeader.Text != "Chat: Lobby (voice)" || !a.chatEntry.Disabled() {
		t.Fatal("selecting Games must not make Lobby chat look like Games chat")
	}
	a.selectedChannelID = 1
	a.updateChatContext(1)
	if a.chatEntry.Disabled() {
		t.Fatal("chat must be available when the selected channel matches voice")
	}
	a.updateChatContext(0)
	if a.chatHeader.Text != "Join voice to chat" || !a.chatEntry.Disabled() {
		t.Fatal("chat must be unavailable outside voice")
	}
	a.channels[0].Name = strings.Repeat("W", 64)
	a.selectedChannelID = 1
	a.updateChatContext(1)
	if width := a.window.Content().MinSize().Width; width > 800 {
		t.Fatalf("long channel name makes window %.0f px wide", width)
	}
}
