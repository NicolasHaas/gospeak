package ui

import (
	"errors"
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
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
