package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
)

func trustDisclosureObjects(root fyne.CanvasObject) []fyne.CanvasObject {
	if root == nil || !root.Visible() {
		return nil
	}
	objects := []fyne.CanvasObject{root}
	switch v := root.(type) {
	case *fyne.Container:
		for _, child := range v.Objects {
			objects = append(objects, trustDisclosureObjects(child)...)
		}
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(v).Objects() {
			objects = append(objects, trustDisclosureObjects(child)...)
		}
	}
	return objects
}

func TestPendingCredentialTrustMobileRenderer(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("trust"), bookmarks: client.NewBookmarkStore()}
	defer a.window.Close()
	a.window.SetContent(container.NewWithoutLayout())
	a.window.Resize(fyne.NewSize(320, 640))
	p := client.PendingPersonalCredential{ControlAddr: "127.0.0.1:9600", Username: "person", AccountID: 7, ServerIdentity: "SHA256:" + strings.Repeat("A", 43) + "=", OldToken: "synthetic-old", Candidate: strings.Repeat("b", 64), Attempt: strings.Repeat("a", 64)}
	if err := a.bookmarks.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	next := "SHA256:" + strings.Repeat("B", 43) + "="
	status := widget.NewLabel("")
	if !a.confirmPendingCredentialTrust(&client.ServerIdentityChangedError{Addr: p.ControlAddr, Expected: p.ServerIdentity, Received: next}, p, status) {
		t.Fatal("confirmation missing")
	}
	viewport := a.window.Canvas().Size()
	if viewport.Width != 320 {
		t.Fatalf("not a real 320-wide canvas: %v", viewport)
	}
	var disclosure *widget.Label
	var scroll *container.Scroll
	buttons := 0
	for _, overlay := range a.window.Canvas().Overlays().List() {
		for _, o := range trustDisclosureObjects(overlay) {
			switch v := o.(type) {
			case *widget.Button:
				if v.Text != "Cancel" && v.Text != "Re-trust pending server" {
					continue
				}
				buttons++
				pos := f.Driver().AbsolutePositionForObject(v)
				if pos.X < 0 || pos.Y < 0 || pos.X+v.Size().Width > viewport.Width || pos.Y+v.Size().Height > viewport.Height {
					t.Errorf("action clipped: %q %v %v", v.Text, pos, v.Size())
				}
			case *widget.Label:
				if strings.Contains(v.Text, "WARNING:") {
					disclosure = v
				}
			case *container.Scroll:
				scroll = v
			}
		}
	}
	if disclosure == nil || scroll == nil || buttons != 2 {
		t.Fatal("missing native disclosure, scroll or actions")
	}
	if disclosure.Wrapping != fyne.TextWrapBreak || disclosure.Size().Width > viewport.Width {
		t.Fatal("exact fingerprint disclosure cannot wrap within viewport")
	}
	// Inspect actual text rows produced by the native renderer, not just Label.Text.
	var rendered strings.Builder
	for _, o := range trustDisclosureObjects(disclosure) {
		if row, ok := o.(*canvas.Text); ok {
			if row.MinSize().Width > disclosure.Size().Width {
				t.Fatalf("rendered row exceeds disclosure: %q", row.Text)
			}
			rendered.WriteString(row.Text)
		}
	}
	text := strings.ReplaceAll(rendered.String(), " ", "")
	for _, exact := range []string{p.ServerIdentity, next, p.ControlAddr, "#7person", "trustedchannel"} {
		if !strings.Contains(text, exact) {
			t.Fatalf("complete disclosure absent from native rows: %q", exact)
		}
	}
	scroll.ScrollToBottom()
	if scroll.Offset.Y <= 0 && disclosure.Size().Height > scroll.Size().Height {
		t.Fatal("fingerprint tail cannot be scrolled into view")
	}
	buttonsByName, _ := credentialDialogControls(a.window)
	test.Tap(buttonsByName["Cancel"])
	saved, found, err := a.bookmarks.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil || !found || saved != p {
		t.Fatal("layout/cancel changed pending credentials")
	}
}
