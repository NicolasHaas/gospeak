package client

import (
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Bookmark represents a saved server connection.
type Bookmark struct {
	Name        string `yaml:"name"`
	ControlAddr string `yaml:"control_addr"`
	VoiceAddr   string `yaml:"voice_addr"`
	Username    string `yaml:"username"`
	Token       string `yaml:"token"`
	LastUsed    int64  `yaml:"last_used,omitempty"`
}

// BookmarkStore manages server bookmarks stored in the user config directory.
type BookmarkStore struct {
	path              string
	legacyPath        string
	Bookmarks         []Bookmark        `yaml:"bookmarks"`
	TrustedServerPins map[string]string `yaml:"trusted_server_pins,omitempty"`
}

// NewBookmarkStore creates a bookmark store in the user config directory.
func NewBookmarkStore() *BookmarkStore {
	legacyPath := legacyFilePath("servers.yaml")
	path, err := configFilePath("servers.yaml")
	if err != nil {
		slog.Error("resolve bookmark path", "err", err)
		path = legacyPath
	}
	return &BookmarkStore{
		path:       path,
		legacyPath: legacyPath,
	}
}

// Load reads bookmarks from disk. Returns empty list if file doesn't exist.
func (bs *BookmarkStore) Load() error {
	data, err := os.ReadFile(bs.path)
	if err != nil {
		if !os.IsNotExist(err) || bs.legacyPath == "" || bs.legacyPath == bs.path {
			if os.IsNotExist(err) {
				bs.Bookmarks = nil
				return nil
			}
			return err
		}
		data, err = os.ReadFile(bs.legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				bs.Bookmarks = nil
				return nil
			}
			return err
		}
		if err := yaml.Unmarshal(data, bs); err != nil {
			return err
		}
		if err := bs.Save(); err != nil {
			return err
		}
		if err := os.Remove(bs.legacyPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return yaml.Unmarshal(data, bs)
}

// Save writes bookmarks to disk.
func (bs *BookmarkStore) Save() error {
	data, err := yaml.Marshal(bs)
	if err != nil {
		return err
	}
	return writePrivateFile(bs.path, data)
}

// PinForAddr returns the saved TOFU identity for a control address.
func (bs *BookmarkStore) PinForAddr(controlAddr string) string {
	return bs.TrustedServerPins[controlAddr]
}

// TrustServer records an explicitly accepted TOFU identity for a control
// address. Calling it again is the explicit re-trust operation.
func (bs *BookmarkStore) TrustServer(controlAddr, fingerprint string) {
	if bs.TrustedServerPins == nil {
		bs.TrustedServerPins = make(map[string]string)
	}
	bs.TrustedServerPins[controlAddr] = fingerprint
}

// Add adds or updates a bookmark. Returns true if it was a new entry.
func (bs *BookmarkStore) Add(b Bookmark) bool {
	for i, existing := range bs.Bookmarks {
		if existing.ControlAddr == b.ControlAddr && existing.Username == b.Username {
			bs.Bookmarks[i] = b
			return false
		}
	}
	bs.Bookmarks = append(bs.Bookmarks, b)
	return true
}

// Touch updates LastUsed for an existing bookmark.
func (bs *BookmarkStore) Touch(controlAddr, username string, ts int64) bool {
	for i := range bs.Bookmarks {
		if bs.Bookmarks[i].ControlAddr == controlAddr && bs.Bookmarks[i].Username == username {
			bs.Bookmarks[i].LastUsed = ts
			return true
		}
	}
	return false
}

// FindByAddr returns a bookmark matching the given control address, or nil.
func (bs *BookmarkStore) FindByAddr(controlAddr string) *Bookmark {
	for _, b := range bs.Bookmarks {
		if b.ControlAddr == controlAddr {
			return &b
		}
	}
	return nil
}
