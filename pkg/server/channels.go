package server

import (
	"sync"
)

// ChannelManager manages voice channels and their members.
type ChannelManager struct {
	mu      sync.RWMutex
	members map[int64]map[uint32]bool // channelID -> set of sessionIDs
}

// NewChannelManager creates a new channel manager.
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		members: make(map[int64]map[uint32]bool),
	}
}

// Join adds a session to a channel, removing from any previous channel.
func (cm *ChannelManager) Join(sessionID uint32, channelID int64) (prevChannelID int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Remove from current channel if any
	for chID, sessions := range cm.members {
		if sessions[sessionID] {
			delete(sessions, sessionID)
			prevChannelID = chID
			if len(sessions) == 0 {
				delete(cm.members, chID)
			}
			break
		}
	}

	// Add to new channel
	if _, ok := cm.members[channelID]; !ok {
		cm.members[channelID] = make(map[uint32]bool)
	}
	cm.members[channelID][sessionID] = true
	return prevChannelID
}

// Leave removes a session from its current channel.
func (cm *ChannelManager) Leave(sessionID uint32) (channelID int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for chID, sessions := range cm.members {
		if sessions[sessionID] {
			delete(sessions, sessionID)
			if len(sessions) == 0 {
				delete(cm.members, chID)
			}
			return chID
		}
	}
	return 0
}

// Members returns all session IDs in a channel.
func (cm *ChannelManager) Members(channelID int64) []uint32 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	sessions := cm.members[channelID]
	result := make([]uint32, 0, len(sessions))
	for sid := range sessions {
		result = append(result, sid)
	}
	return result
}

// ChannelOf returns the channel ID a session is in, or 0 if none.
func (cm *ChannelManager) ChannelOf(sessionID uint32) int64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for chID, sessions := range cm.members {
		if sessions[sessionID] {
			return chID
		}
	}
	return 0
}

// MembersCount returns how many users are in a channel.
func (cm *ChannelManager) MembersCount(channelID int64) int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.members[channelID])
}
