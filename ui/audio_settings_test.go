package ui

import (
	"runtime"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
)

// The Fyne test driver runs Do inline; queue completion explicitly so this
// asynchronous Apply test keeps all widget/settings access on its test thread.
type audioSettingsDriver struct {
	fyne.Driver
	completion chan func()
}

func (d *audioSettingsDriver) DoFromGoroutine(fn func(), _ bool) { d.completion <- fn }

type audioSettingsApp struct {
	fyne.App
	driver fyne.Driver
}

func (a *audioSettingsApp) Driver() fyne.Driver { return a.driver }

func isolateAudioSettingsConfig(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", root)
	case "darwin", "ios":
		t.Setenv("HOME", root)
	case "plan9":
		t.Setenv("home", root)
	default:
		t.Setenv("XDG_CONFIG_HOME", root)
	}
}

func TestDefaultServerAddress(t *testing.T) {
	addr, _, err := normalizeAddr(defaultServerHost, defaultControlPort)
	if err != nil || addr != "gospeak.dev:9600" || deriveVoiceAddr(addr) != "gospeak.dev:9601" {
		t.Fatalf("fresh defaults: %q / %v", addr, err)
	}
	addr, _, err = normalizeAddr("private.example:9700", defaultControlPort)
	if err != nil || addr != "private.example:9700" {
		t.Fatal("explicit server address changed")
	}
}

func TestAudioSettingsApplySavesOnlyAcceptedSelection(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine(), settings: client.DefaultSettings(), hotkeys: client.NewGlobalHotkeys()}
	a.buildUI()
	a.showSettingsDialog()
	selects, _, apply := audioSettingsControls(t, a.window)
	selects[0].Options = append(selects[0].Options, "selected input")
	selects[0].SetSelected("selected input")
	selects[1].Options = append(selects[1].Options, "selected output")
	selects[1].SetSelected("selected output")
	// A second Apply while native setup is pending must not replace preferences.
	a.audioApplying = true
	test.Tap(apply)
	if a.settings.AudioInput != "" || a.settings.AudioOutput != "" {
		t.Fatal("pending Apply changed preferences")
	}
	a.audioApplying = false
	a.showSettingsDialog()
	selects, _, apply = audioSettingsControls(t, a.window)
	selects[0].Options = append(selects[0].Options, "accepted input")
	selects[0].SetSelected("accepted input")
	selects[1].Options = append(selects[1].Options, "accepted output")
	selects[1].SetSelected("accepted output")
	completion := make(chan func(), 16)
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completion}})
	defer fyne.SetCurrentApp(f)
	test.Tap(apply)
	finishAudioSettingsApply(t, completion)
	if a.audioApplying {
		t.Fatal("Apply left pending state")
	}
	saved := client.LoadSettings()
	if saved.AudioInput != "accepted input" || saved.AudioOutput != "accepted output" {
		t.Fatalf("saved devices: %q / %q", saved.AudioInput, saved.AudioOutput)
	}
}

func audioSettingsControls(t *testing.T, window fyne.Window) ([]*widget.Select, []*widget.Slider, *widget.Button) {
	t.Helper()
	var selects []*widget.Select
	var sliders []*widget.Slider
	var apply *widget.Button
	var visit func(fyne.CanvasObject)
	visit = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Select:
			selects = append(selects, v)
		case *widget.Slider:
			sliders = append(sliders, v)
		case *widget.Button:
			if v.Text == "Apply" {
				apply = v
			}
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
	for _, o := range window.Canvas().Overlays().List() {
		visit(o)
	}
	if len(selects) < 2 || len(sliders) == 0 || apply == nil {
		t.Fatal("audio settings controls missing")
	}
	return selects, sliders, apply
}

func finishAudioSettingsApply(t *testing.T, completion <-chan func()) {
	t.Helper()
	select {
	case finish := <-completion:
		finish()
	case <-time.After(time.Second):
		t.Fatal("Apply did not complete")
	}
}

func TestAudioSettingsApplyPreservesUnavailableSelection(t *testing.T) {
	for _, change := range []string{"untouched", "input", "output"} {
		t.Run(change, func(t *testing.T) {
			isolateAudioSettingsConfig(t)
			f := test.NewApp()
			defer f.Quit()
			s := client.DefaultSettings()
			s.AudioInput, s.AudioOutput = "unavailable saved input", "unavailable saved output"
			a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine(), settings: s, hotkeys: client.NewGlobalHotkeys()}
			a.buildUI()
			a.showSettingsDialog()
			selects, _, apply := audioSettingsControls(t, a.window)
			wantInput, wantOutput := s.AudioInput, s.AudioOutput
			for i, want := range []string{wantInput, wantOutput} {
				if selects[i].Selected != want {
					t.Errorf("saved device %d selected %q, want %q", i, selects[i].Selected, want)
				}
				count := 0
				for _, option := range selects[i].Options {
					if option == want {
						count++
					}
				}
				if count != 1 {
					t.Errorf("saved device %q occurs %d times", want, count)
				}
			}
			switch change {
			case "input":
				selects[0].SetSelected("(Default)")
				wantInput = ""
			case "output":
				selects[1].SetSelected("(Default)")
				wantOutput = ""
			}
			completion := make(chan func(), 16)
			fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completion}})
			defer fyne.SetCurrentApp(f)
			test.Tap(apply)
			finishAudioSettingsApply(t, completion)
			saved := client.LoadSettings()
			if saved.AudioInput != wantInput || saved.AudioOutput != wantOutput {
				t.Fatalf("saved devices %q / %q, want %q / %q", saved.AudioInput, saved.AudioOutput, wantInput, wantOutput)
			}
		})
	}
}

func TestAudioSettingsApplySavesPreferencesBeforeCompletion(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	s := client.DefaultSettings()
	s.AudioInput, s.AudioOutput = "previous input", "previous output"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// These accepted keys are also persisted on platforms without global hotkeys.
	s.MuteKey, s.DeafenKey = "F9", "F10"
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine(), settings: s, hotkeys: client.NewGlobalHotkeys()}
	a.buildUI()
	a.showSettingsDialog()
	selects, sliders, apply := audioSettingsControls(t, a.window)
	selects[0].SetSelected("(Default)")
	selects[1].SetSelected("(Default)")
	if len(sliders) != 3 {
		t.Fatalf("sliders = %d, want input/output/VAD", len(sliders))
	}
	sliders[0].SetValue(0)
	sliders[1].SetValue(50)
	sliders[2].SetValue(500)
	completion := make(chan func(), 16)
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completion}})
	defer fyne.SetCurrentApp(f)
	test.Tap(apply)
	saved := client.LoadSettings()
	if saved.InputVolume != 0 || saved.OutputVolume != 50 || saved.VADThreshold != 500 || saved.MuteKey != "F9" || saved.DeafenKey != "F10" {
		t.Errorf("accepted preferences not saved before completion: %+v", saved)
	}
	if saved.AudioInput != "previous input" || saved.AudioOutput != "previous output" {
		t.Errorf("pending device preferences replaced: %+v", saved)
	}
	finishAudioSettingsApply(t, completion)
	saved = client.LoadSettings()
	if saved.AudioInput != "" || saved.AudioOutput != "" {
		t.Fatalf("successful device preferences not saved: %+v", saved)
	}
}
