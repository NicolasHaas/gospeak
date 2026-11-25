package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestClearedScreenShareRestoresChat(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	for _, tc := range []struct {
		name  string
		event *pb.ScreenShareEvent
	}{
		{name: "cleared"},
		{name: "inactive", event: &pb.ScreenShareEvent{Active: false}},
		{name: "active", event: &pb.ScreenShareEvent{Active: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.watchingScreenFeed = true
			a.showScreenPanel()
			a.chatEntry.SetText("keep draft")
			a.engine.OnScreenShareEvent(tc.event)
			fyne.DoAndWait(func() {})
			watching := tc.event != nil && tc.event.Active
			if a.watchingScreenFeed != watching || a.screenBox.Visible() != watching ||
				a.chatPane.Visible() == watching || a.chatEntry.Visible() == watching || a.chatSend.Visible() == watching {
				t.Fatalf("watching=%t screen=%t chat=%t entry=%t send=%t; want watching=%t",
					a.watchingScreenFeed, a.screenBox.Visible(), a.chatPane.Visible(), a.chatEntry.Visible(), a.chatSend.Visible(), watching)
			}
			if a.activeScreenShare != tc.event || a.chatEntry.Text != "keep draft" {
				t.Fatal("screen callback lost event identity or chat draft")
			}
		})
	}
}
