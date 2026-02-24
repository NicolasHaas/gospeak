package client

import (
	"os"
	"path/filepath"

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

// BookmarkStore manages server bookmarks stored next to the binary.
type BookmarkStore struct {
	path      string
	Bookmarks []Bookmark `yaml:"bookmarks"`
}

// NewBookmarkStore creates a bookmark store using a file next to the executable.
func NewBookmarkStore() *BookmarkStore {
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	dir := filepath.Dir(exePath)
	return &BookmarkStore{
		path: filepath.Join(dir, "servers.yaml"),
	}
}

// Load reads bookmarks from disk. Returns empty list if file doesn't exist.
func (bs *BookmarkStore) Load() error {
	data, err := os.ReadFile(bs.path)
	if err != nil {
		if os.IsNotExist(err) {
			bs.Bookmarks = nil
			return nil
		}
		return err
	}
	return yaml.Unmarshal(data, bs)
}

// Save writes bookmarks to disk.
func (bs *BookmarkStore) Save() error {
	data, err := yaml.Marshal(bs)
	if err != nil {
		return err
	}
	return os.WriteFile(bs.path, data, 0600)
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
