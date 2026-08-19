package server

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"gopkg.in/yaml.v3"
)

const (
	maxChannelImportDepth = 8
	maxChannelImportCount = 256
	maxChannelImportBytes = protocol.MaxControlMessage
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

// LoadChannelsFromYAML reads a channels YAML file and creates missing channels.
func LoadChannelsFromYAML(path string, st datastore.DataProviderFactory) error {
	data, err := readChannelsYAML(path)
	if err != nil {
		return err
	}
	return ImportChannelsFromYAML(data, st)
}

// ValidateChannelsFile checks a channel configuration without changing the datastore.
func ValidateChannelsFile(path string) error {
	data, err := readChannelsYAML(path)
	if err != nil {
		return err
	}
	_, err = parseChannelsYAML(data)
	return err
}

// ImportChannelsFromYAML parses YAML data and creates missing channels atomically.
func ImportChannelsFromYAML(data []byte, st datastore.DataProviderFactory) error {
	cfg, err := parseChannelsYAML(data)
	if err != nil {
		return err
	}
	return applyChannelsConfig(cfg, st)
}

func readChannelsYAML(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // path from user-provided CLI config
	if err != nil {
		return nil, fmt.Errorf("read channels config: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxChannelImportBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read channels config: %w", err)
	}
	if len(data) > maxChannelImportBytes {
		return nil, fmt.Errorf("read channels config: maximum size %d bytes exceeded", maxChannelImportBytes)
	}
	return data, nil
}

func parseChannelsYAML(data []byte) (ChannelsConfig, error) {
	if len(data) > maxChannelImportBytes {
		return ChannelsConfig{}, fmt.Errorf("parse channels config: maximum size %d bytes exceeded", maxChannelImportBytes)
	}
	var document yaml.Node
	nodeDecoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := nodeDecoder.Decode(&document); err != nil {
		return ChannelsConfig{}, fmt.Errorf("parse channels config: %w", err)
	}
	if err := validateChannelsDocument(&document); err != nil {
		return ChannelsConfig{}, err
	}
	if hasYAMLAliasOrMerge(&document) {
		return ChannelsConfig{}, fmt.Errorf("parse channels config: YAML aliases and merges are not allowed")
	}
	var extra yaml.Node
	if err := nodeDecoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return ChannelsConfig{}, fmt.Errorf("parse channels config: %w", err)
		}
		return ChannelsConfig{}, fmt.Errorf("parse channels config: multiple YAML documents are not allowed")
	}

	var cfg ChannelsConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return ChannelsConfig{}, fmt.Errorf("parse channels config: %w", err)
	}
	if err := validateChannelImport(cfg.Channels); err != nil {
		return ChannelsConfig{}, err
	}
	return cfg, nil
}

func validateChannelsDocument(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return fmt.Errorf("parse channels config: expected one document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("parse channels config: root must be a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "channels" {
			if root.Content[i+1].Kind != yaml.SequenceNode {
				return fmt.Errorf("parse channels config: channels must be a sequence")
			}
			return nil
		}
	}
	return fmt.Errorf("parse channels config: channels field is required")
}

func hasYAMLAliasOrMerge(root *yaml.Node) bool {
	stack := []*yaml.Node{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
			return true
		}
		stack = append(stack, node.Content...)
	}
	return false
}

func applyChannelsConfig(cfg ChannelsConfig, st datastore.DataProviderFactory) error {
	for _, ch := range cfg.Channels {
		if err := ensureChannel(st, ch, 0); err != nil {
			return fmt.Errorf("apply channel import: %w", err)
		}
	}
	slog.Info("imported channels from YAML", "count", countChannels(cfg.Channels))
	return nil
}

func validateChannelImport(channels []ChannelYAML) error {
	count := 0
	var validate func([]ChannelYAML, int) error
	validate = func(entries []ChannelYAML, depth int) error {
		if depth > maxChannelImportDepth {
			return fmt.Errorf("validate channels config: maximum depth %d exceeded", maxChannelImportDepth)
		}
		siblings := make(map[string]struct{}, len(entries))
		for _, ch := range entries {
			if _, exists := siblings[ch.Name]; exists {
				return fmt.Errorf("validate channels config: duplicate channel %q under the same parent", ch.Name)
			}
			siblings[ch.Name] = struct{}{}
			count++
			if count > maxChannelImportCount {
				return fmt.Errorf("validate channels config: maximum channel count %d exceeded", maxChannelImportCount)
			}
			candidate := &model.Channel{
				Name:             ch.Name,
				Description:      ch.Description,
				MaxUsers:         ch.MaxUsers,
				AllowSubChannels: ch.AllowSubChannels,
			}
			if err := candidate.Validate(); err != nil {
				return fmt.Errorf("validate channel %q: %w", ch.Name, err)
			}
			if len(ch.Channels) > 0 {
				if err := validate(ch.Channels, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return validate(channels, 1)
}

func ensureChannel(st datastore.DataProviderFactory, ch ChannelYAML, parentID int64) error {
	// Check if channel already exists under this parent
	existing, err := st.NonTx().GetChannelByNameAndParent(ch.Name, parentID)
	if err != nil {
		return err
	}

	var channelID int64
	if existing != nil {
		channelID = existing.ID
	} else {
		channel := &model.Channel{
			Name:             ch.Name,
			Description:      ch.Description,
			MaxUsers:         ch.MaxUsers,
			ParentID:         parentID,
			IsTemp:           false,
			AllowSubChannels: ch.AllowSubChannels,
		}
		if err := st.NonTx().CreateChannel(channel); err != nil {
			return err
		}
		channelID = channel.ID
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
func ExportChannelsYAML(st datastore.DataProviderFactory) ([]byte, error) {
	channels, err := st.NonTx().ListChannels()
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
func ExportUsersYAML(st datastore.DataProviderFactory) ([]byte, error) {
	users, err := st.NonTx().ListUsers()
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
