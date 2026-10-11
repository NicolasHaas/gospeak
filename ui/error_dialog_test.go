package ui

import (
	"errors"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
)

// A hostile server spamming ErrorResponse frames must not bury the UI under
// modal dialogs: consecutive errors reuse one dialog whose text updates.
func TestErrorDialogsAreCoalesced(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	a := &App{fyneApp: fyneApp, window: fyneApp.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	if a.engine.OnError == nil {
		t.Fatal("OnError callback not bound")
	}

	a.engine.OnError(errors.New("first failure"))
	if got := a.window.Canvas().Overlays().List(); len(got) != 1 {
		t.Fatalf("overlays after first error = %d, want 1", len(got))
	}
	for i := 0; i < 50; i++ {
		a.engine.OnError(errors.New("repeated failure"))
	}
	overlays := a.window.Canvas().Overlays().List()
	if len(overlays) != 1 {
		t.Fatalf("overlays after 50 errors = %d, want 1", len(overlays))
	}
	// The single dialog must show the newest error text.
	newest := false
	var visit func(o fyne.CanvasObject)
	visit = func(o fyne.CanvasObject) {
		if label, ok := o.(*widget.Label); ok && label.Text == "repeated failure" {
			newest = true
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
	for _, o := range overlays {
		visit(o)
	}
	if !newest {
		t.Fatal("coalesced dialog does not show the newest error text")
	}
}
