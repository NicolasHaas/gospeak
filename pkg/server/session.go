package server

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

// SessionManager manages active client sessions.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[uint32]*model.Session // sessionID -> session
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[uint32]*model.Session),
	}
}

// Create creates a new session for an authenticated user.
func (sm *SessionManager) Create(userID int64, username string, role model.Role) *model.Session {
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
		ID:       id,
		UserID:   userID,
		Username: username,
		Role:     role,
	}
	sm.sessions[id] = sess
	return sess
}

// Get retrieves a session by ID.
func (sm *SessionManager) Get(id uint32) *model.Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[id]
}

// GetByUserID retrieves a session by user ID.
func (sm *SessionManager) GetByUserID(userID int64) *model.Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, s := range sm.sessions {
		if s.UserID == userID {
			return s
		}
	}
	return nil
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
		s.UDPAddr = addr
	}
}

// Count returns the number of active sessions.
func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

// All returns all active sessions (snapshot).
func (sm *SessionManager) All() []*model.Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]*model.Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		result = append(result, s)
	}
	return result
}
