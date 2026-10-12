package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
)

func TestAutoTokenSaveFailureKeepsCopyableCredential(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gospeak", "servers.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const original = "bookmarks: ["
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	store := client.NewBookmarkStore()
	if err := store.Load(); err == nil {
		t.Fatal("malformed bookmark load succeeded")
	}
	a := &App{fyneApp: f, window: f.NewWindow("credentials"), bookmarks: store,
		connectServer: "original.test:9600", connectVoice: "voice.test:9701", serverSaved: true}
	callback := a.autoTokenCallback("original-user")
	a.connectAttempt++
	a.connectServer, a.connectToken = "replacement.test:9600", "replacement-token"
	callback("fixture-personal")
	if a.connectToken != "replacement-token" {
		t.Fatal("old credential changed the replacement attempt")
	}
	var copyButton *widget.Button
	visibleToken, visibleFailure, visibleOwner := false, false, false
	var visit func(fyne.CanvasObject)
	visit = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Entry:
			visibleToken = visibleToken || v.Text == "fixture-personal"
		case *widget.Label:
			visibleFailure = visibleFailure || strings.Contains(v.Text, "could not be saved")
			visibleOwner = visibleOwner || strings.Contains(v.Text, "original-user@original.test:9600")
		case *widget.Button:
			if v.Icon == theme.ContentCopyIcon() {
				copyButton = v
			}
		}
		switch v := o.(type) {
		case *fyne.Container:
			for _, child := range v.Objects {
				visit(child)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(v).Objects() {
				visit(child)
			}
		}
	}
	for _, overlay := range a.window.Canvas().Overlays().List() {
		visit(overlay)
	}
	if !visibleToken || !visibleFailure || !visibleOwner || copyButton == nil {
		t.Fatal("save failure did not retain a visible, owned, copyable credential")
	}
	test.Tap(copyButton)
	if f.Clipboard().Content() != "fixture-personal" {
		t.Fatal("copy did not retain the credential")
	}
	data, err := os.ReadFile(path) //nolint:gosec // Fixed fixture path under the isolated test config root.
	if err != nil || string(data) != original {
		t.Fatal("failed credential save overwrote unreadable bookmarks")
	}
}
