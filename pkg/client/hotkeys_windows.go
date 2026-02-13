//go:build windows

package client

import (
	"sync"
	"syscall"
	"time"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

var vkCodes = map[string]int{
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73,
	"F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77,
	"F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
}

// KeyNameToVK converts a key name to a Windows virtual key code.
func KeyNameToVK(name string) int {
	if code, ok := vkCodes[name]; ok {
		return code
	}
	return 0
}

// GlobalHotkeys polls for global key presses on Windows using GetAsyncKeyState.
type GlobalHotkeys struct {
	OnMuteToggle   func()
	OnDeafenToggle func()
	muteVK         int
	deafenVK       int
	mu             sync.Mutex
	stopCh         chan struct{}
	running        bool
}

// NewGlobalHotkeys creates a new GlobalHotkeys instance.
func NewGlobalHotkeys() *GlobalHotkeys {
	return &GlobalHotkeys{
		stopCh: make(chan struct{}),
	}
}

// SetKeys updates the hotkey bindings.
func (g *GlobalHotkeys) SetKeys(muteKey, deafenKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.muteVK = KeyNameToVK(muteKey)
	g.deafenVK = KeyNameToVK(deafenKey)
}

func isKeyDown(vk int) bool {
	ret, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return ret&0x8000 != 0
}

// Start begins polling for hotkey presses in a background goroutine.
func (g *GlobalHotkeys) Start() {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()

	go func() {
		var muteWasDown, deafenWasDown bool
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-g.stopCh:
				return
			case <-ticker.C:
				g.mu.Lock()
				muteVK := g.muteVK
				deafenVK := g.deafenVK
				g.mu.Unlock()

				if muteVK > 0 {
					down := isKeyDown(muteVK)
					if down && !muteWasDown && g.OnMuteToggle != nil {
						g.OnMuteToggle()
					}
					muteWasDown = down
				}

				if deafenVK > 0 {
					down := isKeyDown(deafenVK)
					if down && !deafenWasDown && g.OnDeafenToggle != nil {
						g.OnDeafenToggle()
					}
					deafenWasDown = down
				}
			}
		}
	}()
}

// Stop terminates the hotkey polling loop.
func (g *GlobalHotkeys) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		close(g.stopCh)
		g.running = false
	}
}
