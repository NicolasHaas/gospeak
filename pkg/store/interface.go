package store

import (
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

// DataStore defines the persistence interface for all GoSpeak entities.
// Implementations include the default SQLite store and can be extended to
// support PostgreSQL, in-memory stores for testing, or any other backend.
type DataStore interface {
	// Close closes the underlying storage connection.
	Close() error

	// ZeroTime returns the zero time value (used for no-expiry tokens).
	ZeroTime() time.Time

	// ---- Users ----

	// CreateUser creates a new user and returns it with the assigned ID.
	CreateUser(username string, role model.Role) (*model.User, error)

	// GetUserByUsername retrieves a user by username. Returns (nil, nil) if not found.
	GetUserByUsername(username string) (*model.User, error)

	// GetUserByID retrieves a user by ID. Returns (nil, nil) if not found.
	GetUserByID(id int64) (*model.User, error)

	// GetUserByPersonalTokenHash retrieves a user by personal token hash. Returns (nil, nil) if not found.
	GetUserByPersonalTokenHash(hash string) (*model.User, error)

	// UpdateUserRole changes a user's role.
	UpdateUserRole(userID int64, role model.Role) error

	// UpdateUserPersonalToken sets the personal token hash and timestamp for a user.
	UpdateUserPersonalToken(userID int64, hash string, createdAt time.Time) error

	// ListUsers returns all users.
	ListUsers() ([]model.User, error)

	// ---- Channels ----

	// CreateChannel creates a new channel with basic fields.
	CreateChannel(channel *model.Channel) error

	// DeleteChannel deletes a channel by ID.
	DeleteChannel(id int64) error

	// ListChannels returns all channels.
	ListChannels() ([]model.Channel, error)

	// GetChannel retrieves a channel by ID. Returns (nil, nil) if not found.
	GetChannel(id int64) (*model.Channel, error)

	// GetChannelByNameAndParent retrieves a channel by name and parent ID.
	GetChannelByNameAndParent(name string, parentID int64) (*model.Channel, error)

	// ---- Tokens ----

	// HasTokens returns true if any tokens exist in the database.
	HasTokens() (bool, error)

	// CreateToken stores a new token (hash only).
	CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error

	// ValidateToken checks if a token hash is valid and returns the associated role.
	// It increments the use count atomically.
	ValidateToken(hash string) (model.Role, error)

	// ---- Bans ----

	// CreateBan adds a ban record.
	CreateBan(userID int64, ip, reason string, bannedBy int64, expiresAt time.Time) error

	// IsUserBanned checks if a user ID is currently banned.
	IsUserBanned(userID int64) (bool, error)
}

// Compile-time check: *Store implements DataStore.
var _ DataStore = (*Store)(nil)
