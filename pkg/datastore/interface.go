package datastore

import (
	"context"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

type DataProviderFactory interface {
	NonTx() DataStore
	Tx(context.Context) (DataStoreTx, error)
}

type DataStoreTx interface {
	DataStore
	TokenTransactionProvider
	Rollback() error
	Commit() error
}

// DataStore defines the persistence interface for all GoSpeak entities.
// Implementations include the default SQLite store and can be extended to
// support PostgreSQL, in-memory stores for testing, or any other backend.
type DataStore interface {
	ConfigReadProvider

	UserReadProvider
	UserWriteProvider

	ChannelReadProvider
	ChannelWriteProvider

	TokenReadProvider
	TokenWriteProvider

	BanReadProvider
	BanWriteProvider

	MessageReadProvider
	MessageWriteProvider
}

// Compile-time check: *Store implements DataStore.
var _ DataProviderFactory = (*ProviderFactory)(nil)

type ConfigReadProvider interface {
	ZeroTime() time.Time
	Close() error
}

type UserReadProvider interface {
	GetUserByUsername(username string) (*model.User, error)
	GetUserByID(id int64) (*model.User, error)
	ListUsers() ([]model.User, error)
}

type UserWriteProvider interface {
	CreateUser(username string, role model.Role) (*model.User, error)
	UpdateUserRole(userID int64, role model.Role) error
}

type ChannelReadProvider interface {
	ListChannels() ([]model.Channel, error)
	GetChannel(id int64) (*model.Channel, error)
	GetChannelByNameAndParent(name string, parentID int64) (*model.Channel, error)
}

type ChannelWriteProvider interface {
	CreateChannel(channel *model.Channel) error
	DeleteChannel(id int64) error
}

type TokenReadProvider interface {
	HasTokens() (bool, error)
}

type TokenWriteProvider interface {
	CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error
}

type TokenTransactionProvider interface {
	ValidateToken(hash string) (model.Role, error)
}

type BanReadProvider interface {
	IsUserBanned(userID int64) (bool, error)
}

type BanWriteProvider interface {
	CreateBan(userID int64, ip, reason string, bannedBy int64, expiresAt time.Time) error
}

type MessageReadProvider interface {
	ListMessages(filters model.MessageFilters) ([]model.Message, error)
}

type MessageWriteProvider interface {
	CreateMessage(message *model.Message) error
	DeleteMessage(messageID int64) error
}
