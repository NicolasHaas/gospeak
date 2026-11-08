package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
)

func TestMalformedSettingsApplyPreservesFileAndShowsRecovery(t *testing.T) {
	isolateAudioSettingsConfig(t)
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gospeak", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("mute_key: F9\ninput_volume: broken\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("settings"), engine: client.NewEngine(), settings: client.LoadSettings(), hotkeys: client.NewGlobalHotkeys()}
	a.buildUI()
	a.showSettingsDialog()
	_, sliders, apply := audioSettingsControls(t, a.window)
	sliders[0].SetValue(50)
	completion := make(chan func(), 16)
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completion}})
	defer fyne.SetCurrentApp(f)
	test.Tap(apply)
	finishAudioSettingsApply(t, completion)
	if !a.errorVisible || a.errorLabel == nil || !strings.Contains(a.errorLabel.Text, "refusing to overwrite") || !strings.Contains(a.errorLabel.Text, path) || !strings.Contains(a.errorLabel.Text, "restart") {
		t.Error("Apply did not show the settings preservation error and recovery instructions")
	}
	data, err := os.ReadFile(path) //nolint:gosec // Fixed fixture under the isolated test config root.
	if err != nil || string(data) != string(original) {
		t.Fatal("Apply overwrote malformed settings")
	}
	if a.settings.InputVolume != 50 {
		t.Fatal("runtime preference change was lost")
	}
}
