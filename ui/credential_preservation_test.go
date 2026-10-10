package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
)

func TestAutoTokenOwnedByUIQueue(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	defer fyne.SetCurrentApp(f)
	driver := &audioSettingsDriver{Driver: f.Driver(), completion: make(chan func(), 20)}
	wrapped := &audioSettingsApp{App: f, driver: driver}
	fyne.SetCurrentApp(wrapped)
	a := &App{fyneApp: wrapped, window: f.NewWindow("credentials"), engine: client.NewEngine(), bookmarks: client.NewBookmarkStore(), settings: client.DefaultSettings()}
	a.buildUI()
	a.bindEvents()
	a.connectServer = "example.test:9600"
	a.connectVoice = "voice.test:9701"
	a.connectToken = "fixture-invite"
	a.serverSaved = true
	a.autoTokenCallback("fixture-user")("fixture-personal")
	if a.connectToken != "fixture-invite" {
		t.Error("engine callback mutated credential outside UI queue")
	}
	// The completion may save first; the queued personal token must win last.
	if err := a.saveCurrentBookmark("fixture-user"); err != nil {
		t.Fatal(err)
	}
	(<-driver.completion)()
	loaded := client.NewBookmarkStore()
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	b := loaded.FindByAddr(a.connectServer)
	if b == nil || b.Token != "fixture-personal" || b.VoiceAddr != a.connectVoice {
		t.Fatal("personal credential or advanced voice target lost")
	}
	// The opposite scheduling order must not restore the invite snapshot either.
	if err := a.saveCurrentBookmark("fixture-user"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if b := loaded.FindByAddr(a.connectServer); b == nil || b.Token != "fixture-personal" {
		t.Fatal("late completion restored stale invite")
	}
	// An already queued callback must not borrow a replacement target.
	old := a.autoTokenCallback("fixture-user")
	old("fixture-stale")
	a.connectAttempt++
	a.connectServer = "other.test:9600"
	a.connectToken = "fixture-other"
	(<-driver.completion)()
	if a.connectToken != "fixture-other" || a.bookmarks.FindByAddr(a.connectServer) != nil {
		t.Fatal("stale callback changed replacement credentials")
	}
	// Declining persistence still updates memory and displays the token.
	a.serverSaved = false
	a.autoTokenCallback("other-user")("fixture-unsaved")
	(<-driver.completion)()
	if a.connectToken != "fixture-unsaved" || a.bookmarks.FindByAddr(a.connectServer) != nil {
		t.Fatal("opt-out did not preserve user intent")
	}
}
