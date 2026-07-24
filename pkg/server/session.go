package server

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"net"
	"sync"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

// SessionManager manages active client sessions.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[uint32]*model.Session // sessionID -> session
}

// SessionSnapshot is an immutable view of a session.
type SessionSnapshot struct {
	ID              uint32
	UserID          int64
	Username        string
	Role            model.Role
	ChannelScope    int64
	ChannelID       int64
	ScreenAuthToken string
	UDPAddr         *net.UDPAddr
	Muted           bool
	Deafened        bool
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[uint32]*model.Session),
	}
}

// Create creates a new session for an authenticated user.
func (sm *SessionManager) Create(userID int64, username string, role model.Role) *model.Session {
	return sm.CreateWithChannelScope(userID, username, role, 0)
}

// CreateWithChannelScope creates a session with a persistent invite restriction.
func (sm *SessionManager) CreateWithChannelScope(userID int64, username string, role model.Role, channelScope int64) *model.Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Generate random session ID
	var id uint32
	for {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand failure: " + err.Error())
		}
		id = binary.BigEndian.Uint32(b)
		if id != 0 {
			if _, exists := sm.sessions[id]; !exists {
				break
			}
		}
	}

	sess := &model.Session{
		ID:              id,
		UserID:          userID,
		Username:        username,
		Role:            role,
		ChannelScope:    channelScope,
		ScreenAuthToken: newScreenAuthToken(),
	}
	sm.sessions[id] = sess
	return sess
}

// GetSnapshot returns an immutable snapshot of the session by session ID.
func (sm *SessionManager) GetSnapshot(sessionID uint32) (SessionSnapshot, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	if !ok {
		return SessionSnapshot{}, false
	}
	return SessionSnapshot{
		ID:              s.ID,
		UserID:          s.UserID,
		Username:        s.Username,
		Role:            s.Role,
		ChannelScope:    s.ChannelScope,
		ChannelID:       s.ChannelID,
		ScreenAuthToken: s.ScreenAuthToken,
		UDPAddr:         cloneUDPAddr(s.UDPAddr),
		Muted:           s.Muted,
		Deafened:        s.Deafened,
	}, true
}

// GetAllByUserIDSnapshots retrieves immutable snapshots for every session of a user.
func (sm *SessionManager) GetAllByUserIDSnapshots(userID int64) []SessionSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	snapshots := make([]SessionSnapshot, 0)
	for _, s := range sm.sessions {
		if s.UserID == userID {
			snapshots = append(snapshots, SessionSnapshot{
				ID:              s.ID,
				UserID:          s.UserID,
				Username:        s.Username,
				Role:            s.Role,
				ChannelScope:    s.ChannelScope,
				ChannelID:       s.ChannelID,
				ScreenAuthToken: s.ScreenAuthToken,
				UDPAddr:         cloneUDPAddr(s.UDPAddr),
				Muted:           s.Muted,
				Deafened:        s.Deafened,
			})
		}
	}
	return snapshots
}

// ValidateScreenAuth reports whether the session exists and the auth token matches.
func (sm *SessionManager) ValidateScreenAuth(sessionID uint32, token string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	return ok && s.ScreenAuthToken == token
}

// Remove removes a session.
func (sm *SessionManager) Remove(id uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, id)
}

// SetUDPAddr sets the UDP address for a session (called after first voice packet).
func (sm *SessionManager) SetUDPAddr(id uint32, addr *net.UDPAddr) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		s.UDPAddr = cloneUDPAddr(addr)
	}
}

// UpdateUserState updates muted/deafened for a session.
func (sm *SessionManager) UpdateUserState(id uint32, muted, deafened bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		s.Muted = muted
		s.Deafened = deafened
	}
}

// SetChannel sets the channel ID for a session.
func (sm *SessionManager) SetChannel(id uint32, channelID int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		s.ChannelID = channelID
	}
}

// UpdateRoleByUserID updates the role for every active session of a user.
func (sm *SessionManager) UpdateRoleByUserID(userID int64, role model.Role) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, s := range sm.sessions {
		if s.UserID == userID {
			s.Role = role
		}
	}
}

// Count returns the number of active sessions.
func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	clone := *addr
	if addr.IP != nil {
		clone.IP = append([]byte(nil), addr.IP...)
	}
	return &clone
}

func newScreenAuthToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	return hex.EncodeToString(b)
}
