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

// NewLegacyBookmarkStore creates a bookmark store pinned to the legacy
// location. It is used when the user explicitly declines migration.
func NewLegacyBookmarkStore() *BookmarkStore {
	path := legacyFilePath("servers.yaml")
	return &BookmarkStore{path: path, legacyPath: path}
}

// Load reads bookmarks from disk. It falls back to the legacy file without
// migrating it; migration is an explicit user action.
func (bs *BookmarkStore) Load() error {
	loaded := BookmarkStore{path: bs.path, legacyPath: bs.legacyPath}
	bs.Bookmarks = nil
	bs.TrustedServerPins = nil
	data, err := readPrivateFile(bs.path, bs.path != bs.legacyPath)
	if os.IsNotExist(err) && bs.legacyPath != "" && bs.legacyPath != bs.path {
		data, err = readPrivateFile(bs.legacyPath, false)
		loaded.path = bs.legacyPath
	}
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	bs.path = loaded.path
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		return err
	}
	*bs = loaded
	return nil
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
