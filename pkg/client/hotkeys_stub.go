//go:build !windows

package client

// GlobalHotkeys is a no-op on non-Windows platforms.
// Users can still use the mute/deafen buttons in the UI.
type GlobalHotkeys struct {
	OnMuteToggle   func()
	OnDeafenToggle func()
}

// NewGlobalHotkeys creates a new GlobalHotkeys instance (no-op on non-Windows).
func NewGlobalHotkeys() *GlobalHotkeys {
	return &GlobalHotkeys{}
}

// SetKeys is a no-op on non-Windows.
func (g *GlobalHotkeys) SetKeys(muteKey, deafenKey string) {}

// Start is a no-op on non-Windows.
func (g *GlobalHotkeys) Start() {}

// Stop is a no-op on non-Windows.
func (g *GlobalHotkeys) Stop() {}

// KeyNameToVK is a no-op on non-Windows.
func KeyNameToVK(name string) int { return 0 }
