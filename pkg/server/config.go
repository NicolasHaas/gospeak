package server

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/store"
	"gopkg.in/yaml.v3"
)

// ChannelYAML represents a channel in YAML config.
type ChannelYAML struct {
	Name             string        `yaml:"name"`
	Description      string        `yaml:"description,omitempty"`
	MaxUsers         int           `yaml:"max_users,omitempty"`
	AllowSubChannels bool          `yaml:"allow_sub_channels,omitempty"`
	Channels         []ChannelYAML `yaml:"channels,omitempty"` // nested sub-channels
}

// ChannelsConfig is the top-level YAML config for channels.
type ChannelsConfig struct {
	Channels []ChannelYAML `yaml:"channels"`
}

// UserYAML represents a user in YAML export.
type UserYAML struct {
	ID        int64  `yaml:"id"`
	Username  string `yaml:"username"`
	Role      string `yaml:"role"`
	CreatedAt string `yaml:"created_at"`
}

// UsersExport is the top-level YAML for user export.
type UsersExport struct {
	Users []UserYAML `yaml:"users"`
}

// LoadChannelsFromYAML reads a channels YAML file and creates/updates channels in the store.
func LoadChannelsFromYAML(path string, st *store.Store) error {
	data, err := os.ReadFile(path) //nolint:gosec // path from user-provided CLI config
	if err != nil {
		return fmt.Errorf("read channels config: %w", err)
	}
	return ImportChannelsFromYAML(data, st)
}

// ImportChannelsFromYAML parses YAML data and creates/updates channels in the store.
func ImportChannelsFromYAML(data []byte, st *store.Store) error {
	var cfg ChannelsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse channels config: %w", err)
	}

	for _, ch := range cfg.Channels {
		if err := ensureChannel(st, ch, 0); err != nil {
			slog.Error("failed to create channel from config", "name", ch.Name, "err", err)
		}
	}

	slog.Info("imported channels from YAML", "count", countChannels(cfg.Channels))
	return nil
}

func ensureChannel(st *store.Store, ch ChannelYAML, parentID int64) error {
	// Check if channel already exists under this parent
	existing, err := st.GetChannelByNameAndParent(ch.Name, parentID)
	if err != nil {
		return err
	}

	var channelID int64
	if existing != nil {
		channelID = existing.ID
	} else {
		created, err := st.CreateChannelFull(ch.Name, ch.Description, ch.MaxUsers, parentID, false, ch.AllowSubChannels)
		if err != nil {
			return err
		}
		channelID = created.ID
		slog.Debug("created channel from config", "name", ch.Name, "parent", parentID)
	}

	// Recurse into sub-channels
	for _, sub := range ch.Channels {
		if err := ensureChannel(st, sub, channelID); err != nil {
			return err
		}
	}
	return nil
}

func countChannels(channels []ChannelYAML) int {
	count := len(channels)
	for _, ch := range channels {
		count += countChannels(ch.Channels)
	}
	return count
}

// ExportChannelsYAML exports all channels as YAML.
func ExportChannelsYAML(st *store.Store) ([]byte, error) {
	channels, err := st.ListChannels()
	if err != nil {
		return nil, err
	}

	// Build tree
	roots := buildChannelTree(channels, 0)
	cfg := ChannelsConfig{Channels: roots}
	return yaml.Marshal(&cfg)
}

func buildChannelTree(channels []model.Channel, parentID int64) []ChannelYAML {
	var result []ChannelYAML
	for _, ch := range channels {
		if ch.ParentID == parentID && !ch.IsTemp {
			entry := ChannelYAML{
				Name:             ch.Name,
				Description:      ch.Description,
				MaxUsers:         ch.MaxUsers,
				AllowSubChannels: ch.AllowSubChannels,
				Channels:         buildChannelTree(channels, ch.ID),
			}
			result = append(result, entry)
		}
	}
	return result
}

// ExportUsersYAML exports all users as YAML.
func ExportUsersYAML(st *store.Store) ([]byte, error) {
	users, err := st.ListUsers()
	if err != nil {
		return nil, err
	}

	export := UsersExport{}
	for _, u := range users {
		export.Users = append(export.Users, UserYAML{
			ID:        u.ID,
			Username:  u.Username,
			Role:      u.Role.String(),
			CreatedAt: u.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return yaml.Marshal(&export)
}
