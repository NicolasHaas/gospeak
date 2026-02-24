package store

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

// MemoryStore provides an in-memory DataStore implementation for tests.
// It mirrors SQLite behavior for validation and error handling.
type MemoryStore struct {
	mu sync.RWMutex

	now func() time.Time

	nextUserID    int64
	nextChannelID int64
	nextTokenID   int64
	nextBanID     int64

	usersByID       map[int64]*model.User
	usersByUsername map[string]*model.User
	channelsByID    map[int64]*model.Channel
	tokensByHash    map[string]*memoryToken
	bansByID        map[int64]*model.Ban
}

type memoryToken struct {
	id           int64
	hash         string
	role         model.Role
	channelScope int64
	createdBy    int64
	maxUses      int
	useCount     int
	expiresAt    time.Time
	createdAt    time.Time
}

// NewMemory creates a MemoryStore using time.Now().UTC().
func NewMemory() *MemoryStore {
	return NewMemoryWithClock(func() time.Time { return time.Now().UTC() })
}

// NewMemoryWithClock creates a MemoryStore with a custom clock.
func NewMemoryWithClock(now func() time.Time) *MemoryStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryStore{
		now:             now,
		nextUserID:      1,
		nextChannelID:   1,
		nextTokenID:     1,
		nextBanID:       1,
		usersByID:       make(map[int64]*model.User),
		usersByUsername: make(map[string]*model.User),
		channelsByID:    make(map[int64]*model.Channel),
		tokensByHash:    make(map[string]*memoryToken),
		bansByID:        make(map[int64]*model.Ban),
	}
}

// Close is a no-op for MemoryStore.
func (s *MemoryStore) Close() error {
	return nil
}

// ZeroTime returns the zero time value (used for no-expiry tokens).
func (s *MemoryStore) ZeroTime() time.Time {
	return time.Time{}
}

// CreateUser creates a new user and returns it with the assigned ID.
func (s *MemoryStore) CreateUser(username string, role model.Role) (*model.User, error) {
	if err := model.ValidateUsername(username); err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	if !role.Valid() {
		return nil, fmt.Errorf("store: create user: %w", model.ErrInvalidRole)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.usersByUsername[username]; exists {
		return nil, fmt.Errorf("store: create user: constraint failed: UNIQUE constraint failed: users.username")
	}
	createdAt := s.now().UTC()
	user := &model.User{
		ID:                     s.nextUserID,
		Username:               username,
		Role:                   role,
		PersonalTokenHash:      "",
		PersonalTokenCreatedAt: time.Time{},
		CreatedAt:              createdAt,
	}
	s.nextUserID++
	copyUser := *user
	s.usersByID[user.ID] = user
	s.usersByUsername[username] = user
	return &copyUser, nil
}

// GetUserByID retrieves a user by ID.
func (s *MemoryStore) GetUserByID(id int64) (*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.usersByID[id]
	if !ok {
		return nil, nil
	}
	copyUser := *user
	return &copyUser, nil
}

// GetUserByPersonalTokenHash retrieves a user by personal token hash.
func (s *MemoryStore) GetUserByPersonalTokenHash(hash string) (*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.usersByID {
		if user.PersonalTokenHash == hash {
			copyUser := *user
			return &copyUser, nil
		}
	}
	return nil, nil
}

// UpdateUserRole changes a user's role.
func (s *MemoryStore) UpdateUserRole(userID int64, role model.Role) error {
	if !role.Valid() {
		return fmt.Errorf("store: update user role: %w", model.ErrInvalidRole)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.usersByID[userID]
	if !ok {
		return nil
	}
	user.Role = role
	return nil
}

// UpdateUserPersonalToken sets the personal token hash and timestamp for a user.
func (s *MemoryStore) UpdateUserPersonalToken(userID int64, hash string, createdAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.usersByID[userID]
	if !ok {
		return nil
	}
	user.PersonalTokenHash = hash
	if createdAt.IsZero() {
		user.PersonalTokenCreatedAt = time.Time{}
	} else {
		user.PersonalTokenCreatedAt = createdAt.UTC()
	}
	return nil
}

// ListUsers returns all users.
func (s *MemoryStore) ListUsers() ([]model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]model.User, 0, len(s.usersByID))
	for _, user := range s.usersByID {
		users = append(users, *user)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})
	return users, nil
}

// CreateChannel creates a new channel with basic fields.
func (s *MemoryStore) CreateChannel(channel *model.Channel) error {
	if err := channel.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	channel.ID = s.nextChannelID
	channel.CreatedAt = s.now().UTC()
	s.nextChannelID++
	copyChannel := *channel
	s.channelsByID[channel.ID] = &copyChannel
	return nil
}

// DeleteChannel deletes a channel by ID.
func (s *MemoryStore) DeleteChannel(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.channelsByID, id)
	return nil
}

// ListChannels returns all channels.
func (s *MemoryStore) ListChannels() ([]model.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channels := make([]model.Channel, 0, len(s.channelsByID))
	for _, ch := range s.channelsByID {
		channels = append(channels, *ch)
	}
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].ParentID == channels[j].ParentID {
			return channels[i].ID < channels[j].ID
		}
		return channels[i].ParentID < channels[j].ParentID
	})
	return channels, nil
}

// GetChannel retrieves a channel by ID.
func (s *MemoryStore) GetChannel(id int64) (*model.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channelsByID[id]
	if !ok {
		return nil, nil
	}
	copyChannel := *ch
	return &copyChannel, nil
}

// GetChannelByNameAndParent retrieves a channel by name and parent ID.
func (s *MemoryStore) GetChannelByNameAndParent(name string, parentID int64) (*model.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.channelsByID {
		if ch.Name == name && ch.ParentID == parentID {
			copyChannel := *ch
			return &copyChannel, nil
		}
	}
	return nil, nil
}

// HasTokens returns true if any tokens exist in the database.
func (s *MemoryStore) HasTokens() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokensByHash) > 0, nil
}

// CreateToken stores a new token (hash only).
func (s *MemoryStore) CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tokensByHash[hash]; exists {
		return fmt.Errorf("store: create token: constraint failed: UNIQUE constraint failed: tokens.hash")
	}
	createdAt := s.now().UTC()
	if expiresAt.IsZero() {
		expiresAt = time.Time{}
	} else {
		expiresAt = expiresAt.UTC()
	}
	s.tokensByHash[hash] = &memoryToken{
		id:           s.nextTokenID,
		hash:         hash,
		role:         role,
		channelScope: channelScope,
		createdBy:    createdBy,
		maxUses:      maxUses,
		useCount:     0,
		expiresAt:    expiresAt,
		createdAt:    createdAt,
	}
	s.nextTokenID++
	return nil
}

// ValidateToken checks if a token hash is valid and returns the associated role.
// It increments the use count atomically.
func (s *MemoryStore) ValidateToken(hash string) (model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokensByHash[hash]
	if !ok {
		return 0, fmt.Errorf("store: invalid token")
	}

	if !token.expiresAt.IsZero() && s.now().UTC().After(token.expiresAt) {
		return 0, fmt.Errorf("store: token expired")
	}
	if token.maxUses > 0 && token.useCount >= token.maxUses {
		return 0, fmt.Errorf("store: token exhausted")
	}

	token.useCount++
	return token.role, nil
}

// CreateBan adds a ban record.
func (s *MemoryStore) CreateBan(userID int64, ip, reason string, bannedBy int64, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ban := &model.Ban{
		ID:        s.nextBanID,
		UserID:    userID,
		IP:        ip,
		Reason:    reason,
		BannedBy:  bannedBy,
		ExpiresAt: expiresAt,
		CreatedAt: s.now().UTC(),
	}
	s.nextBanID++
	s.bansByID[ban.ID] = ban
	return nil
}

// IsUserBanned checks if a user ID is currently banned.
func (s *MemoryStore) IsUserBanned(userID int64) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UTC()
	for _, ban := range s.bansByID {
		if ban.UserID != userID {
			continue
		}
		if ban.ExpiresAt.IsZero() || ban.ExpiresAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

// Compile-time check: *MemoryStore implements DataStore.
var _ DataStore = (*MemoryStore)(nil)
