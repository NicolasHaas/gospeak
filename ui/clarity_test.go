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

func TestAudioFailureSurvivesActivity(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	a.vadIndicator.SetText("Muted")
	a.updateMuteButtons()
	if a.vadIndicator.Text == "Muted" {
		t.Fatal("unmute left stale muted status")
	}
	a.engine.OnStateChange(client.StateConnected)
	fyne.DoAndWait(func() {})
	a.engine.OnVoiceActivity(true)
	fyne.DoAndWait(func() {})
	if a.vadIndicator.Text == "Speaking" {
		t.Fatal("microphone activity without membership must not claim speaking")
	}
	a.engine.OnAudioFailure(errors.New("no input device"))
	fyne.DoAndWait(func() {})
	a.engine.OnVoiceActivity(true)
	fyne.DoAndWait(func() {})
	if a.vadIndicator.Text != "Audio unavailable" {
		t.Fatalf("activity hid audio failure: %q", a.vadIndicator.Text)
	}
	if !a.muteBtn.Disabled() || !a.deafenBtn.Disabled() {
		t.Fatal("unavailable audio controls must be disabled")
	}
	if !a.audioReason.Visible() || !strings.Contains(a.audioReason.Text, "no input device") || !strings.Contains(a.audioReason.Text, "reconnect") {
		t.Fatal("audio failure needs a persistent reason and supported recovery advice")
	}
	a.engine.OnChannelJoined(2)
	fyne.DoAndWait(func() {})
	if a.voiceStatus.Text != "Voice channel: none" {
		t.Fatal("membership must come from engine state, not a UI selection or callback argument")
	}
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	if a.audioFailed || a.audioReason.Visible() || a.audioReason.Text != "" {
		t.Fatal("disconnect must clear the old audio failure")
	}
	a.engine.OnStateChange(client.StateConnected)
	fyne.DoAndWait(func() {})
	if a.muteBtn.Disabled() || a.deafenBtn.Disabled() || a.vadIndicator.Text != "Audio starting..." {
		t.Fatal("reconnect must restore controls without claiming audio is ready")
	}
}

func TestSendSharesSubmitAndScreenVisibility(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	if !a.chatSend.Disabled() {
		t.Fatal("Send must start disabled")
	}
	a.chatChannelID = 2
	a.updateChatContext()
	a.chatEntry.SetText("draft")
	var submitted string
	a.chatEntry.OnSubmitted = func(text string) { submitted = text }
	test.Tap(a.chatSend)
	if submitted != "draft" {
		t.Fatal("Send bypassed the entry submit path")
	}
	a.showScreenPanel()
	if a.chatSend.Visible() || a.chatEntry.Visible() {
		t.Fatal("screen view must hide both composer controls")
	}
	a.showChatPanel()
	if !a.chatSend.Visible() || !a.chatEntry.Visible() {
		t.Fatal("returning to chat must restore both composer controls")
	}
	a.resetChat()
	if !a.chatSend.Disabled() {
		t.Fatal("reset must disable Send")
	}
}

func TestInputMeterAndReadableUserNames(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	t.Run("input meter", func(t *testing.T) {
		if a.engine.OnRMSLevel == nil {
			t.Fatal("input level callback is not connected")
		}
		a.engine.OnStateChange(client.StateConnected)
		fyne.DoAndWait(func() {})
		for _, tc := range []struct{ rms, want float64 }{{2500, 0.5}, {10000, 1}, {0, 0}} {
			a.engine.OnRMSLevel(tc.rms)
			fyne.DoAndWait(func() {})
			if a.vuMeter.Value != tc.want {
				t.Fatalf("RMS %v: meter = %v, want %v", tc.rms, a.vuMeter.Value, tc.want)
			}
		}
		a.engine.OnRMSLevel(2500)
		fyne.DoAndWait(func() {})
		a.engine.OnAudioFailure(errors.New("no input device"))
		a.engine.OnRMSLevel(2500)
		fyne.DoAndWait(func() {})
		if a.vuMeter.Value != 0 || a.vadIndicator.Text != "Audio unavailable" {
			t.Fatal("audio failure must empty meter and preserve its reason")
		}
		a.vuMeter.SetValue(0.5)
		a.engine.OnStateChange(client.StateDisconnected)
		a.engine.OnRMSLevel(2500)
		fyne.DoAndWait(func() {})
		if a.vuMeter.Value != 0 || a.mediaControls.Visible() {
			t.Fatal("disconnect must reset and hide the meter")
		}
	})
	t.Run("user contrast", func(t *testing.T) {
		a.channels = []pb.ChannelInfo{{ID: 1, Name: "Lobby", Users: []pb.UserInfo{{Username: "Guest"}}}}
		row := a.channelList.CreateItem()
		a.updateChannelListItem(1, row)
		label := row.(*fyne.Container).Objects[2].(*widget.Label)
		if label.Importance != widget.MediumImportance {
			t.Fatal("user names must use normal foreground contrast")
		}
	})
}

func TestChatLoadingAndEmptyStates(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.chatChannelID = 2
	a.chatDeleted = make(map[int64]bool)
	a.chatLoading = true
	a.renderChat()
	check := func(want string) {
		t.Helper()
		if len(a.chatBox.Objects) != 1 {
			t.Fatalf("want %q, got %d rows", want, len(a.chatBox.Objects))
		}
		if label, ok := a.chatBox.Objects[0].(*widget.Label); !ok || label.Text != want {
			t.Fatalf("want chat state %q, got %#v", want, a.chatBox.Objects[0])
		}
	}
	check("Loading messages...")
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 1})
	check("Loading messages...")
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2})
	check("No messages yet.")
}
