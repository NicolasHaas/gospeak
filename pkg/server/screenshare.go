package server

import (
	"fmt"
	"math"
	"sync"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type ScreenShareManager struct {
	mu                 sync.RWMutex
	activeByChannel    map[int64]*pb.ScreenShareEvent
	channelBySession   map[uint32]int64
	subscribersByShare map[uint32]map[uint32]bool
	viewerTarget       map[uint32]uint32
	lastFrameAt        map[uint32]time.Time
}

func NewScreenShareManager() *ScreenShareManager {
	return &ScreenShareManager{
		activeByChannel:    make(map[int64]*pb.ScreenShareEvent),
		channelBySession:   make(map[uint32]int64),
		subscribersByShare: make(map[uint32]map[uint32]bool),
		viewerTarget:       make(map[uint32]uint32),
		lastFrameAt:        make(map[uint32]time.Time),
	}
}

func (m *ScreenShareManager) Start(channelID int64, sessionID uint32, userID int64, username string, width, height int32) (*pb.ScreenShareEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if active, ok := m.activeByChannel[channelID]; ok && active.Active && active.SessionID != sessionID {
		return nil, fmt.Errorf("channel already has an active screen share")
	}
	key, err := gospeakCrypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate screen share key: %w", err)
	}

	event := &pb.ScreenShareEvent{
		ChannelID:     channelID,
		SessionID:     sessionID,
		UserID:        userID,
		Username:      username,
		Active:        true,
		Width:         width,
		Height:        height,
		EncryptionKey: append([]byte(nil), key...),
	}
	m.activeByChannel[channelID] = event
	m.channelBySession[sessionID] = channelID
	if _, ok := m.subscribersByShare[sessionID]; !ok {
		m.subscribersByShare[sessionID] = make(map[uint32]bool)
	}
	return cloneScreenShareEvent(event), nil
}

func (m *ScreenShareManager) StopBySession(sessionID uint32) (*pb.ScreenShareEvent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	channelID, ok := m.channelBySession[sessionID]
	if !ok {
		m.removeViewerLocked(sessionID)
		return nil, false
	}

	active, ok := m.activeByChannel[channelID]
	if !ok || active.SessionID != sessionID {
		delete(m.channelBySession, sessionID)
		m.removeViewerLocked(sessionID)
		return nil, false
	}

	delete(m.activeByChannel, channelID)
	delete(m.channelBySession, sessionID)
	delete(m.lastFrameAt, sessionID)
	for viewer := range m.subscribersByShare[sessionID] {
		delete(m.viewerTarget, viewer)
	}
	delete(m.subscribersByShare, sessionID)

	stopped := cloneScreenShareEvent(active)
	stopped.Active = false
	stopped.Viewers = 0
	return stopped, true

}

func (m *ScreenShareManager) Subscribe(channelID int64, viewerSessionID uint32) (*pb.ScreenShareEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active, ok := m.activeByChannel[channelID]
	if !ok || !active.Active {
		return nil, fmt.Errorf("no active screen share in this channel")
	}

	m.removeViewerLocked(viewerSessionID)
	if _, ok := m.subscribersByShare[active.SessionID]; !ok {
		m.subscribersByShare[active.SessionID] = make(map[uint32]bool)
	}
	m.subscribersByShare[active.SessionID][viewerSessionID] = true
	m.viewerTarget[viewerSessionID] = active.SessionID
	viewerCount := len(m.subscribersByShare[active.SessionID])
	if viewerCount > math.MaxInt32 {
		return nil, fmt.Errorf("too many screen share viewers: %d", viewerCount)
	}
	active.Viewers = int32(viewerCount)
	return cloneScreenShareEvent(active), nil
}

func (m *ScreenShareManager) Unsubscribe(viewerSessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeViewerLocked(viewerSessionID)
}

func (m *ScreenShareManager) ActiveForChannel(channelID int64) (*pb.ScreenShareEvent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	active, ok := m.activeByChannel[channelID]
	if !ok {
		return nil, false
	}
	clone := cloneScreenShareEvent(active)
	clone.EncryptionKey = nil
	return clone, true
}

func (m *ScreenShareManager) IsSharer(sessionID uint32, channelID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	active, ok := m.activeByChannel[channelID]
	return ok && active.Active && active.SessionID == sessionID
}

func (m *ScreenShareManager) SubscribersForSharer(sessionID uint32) []uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.subscribersByShare[sessionID]
	result := make([]uint32, 0, len(set))
	for viewer := range set {
		result = append(result, viewer)
	}
	return result
}

func (m *ScreenShareManager) AllowFrame(sessionID uint32, minInterval time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	last := m.lastFrameAt[sessionID]
	if !last.IsZero() && now.Sub(last) < minInterval {
		return false
	}
	m.lastFrameAt[sessionID] = now
	return true
}

func (m *ScreenShareManager) PublicEvent(channelID int64) (*pb.ScreenShareEvent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	active, ok := m.activeByChannel[channelID]
	if !ok {
		return nil, false
	}
	clone := cloneScreenShareEvent(active)
	clone.EncryptionKey = nil
	return clone, true
}

func (m *ScreenShareManager) SubscriberCount() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.viewerTarget))
}

func (m *ScreenShareManager) removeViewerLocked(viewerSessionID uint32) {
	sharerSessionID, ok := m.viewerTarget[viewerSessionID]
	if !ok {
		return
	}
	delete(m.viewerTarget, viewerSessionID)
	if viewers, ok := m.subscribersByShare[sharerSessionID]; ok {
		delete(viewers, viewerSessionID)
		for _, active := range m.activeByChannel {
			if active.SessionID == sharerSessionID {
				viewerCount := len(viewers)
				if viewerCount > math.MaxInt32 {
					active.Viewers = math.MaxInt32
				} else {
					active.Viewers = int32(viewerCount)
				}
				break
			}
		}
		if len(viewers) == 0 {
			delete(m.subscribersByShare, sharerSessionID)
		}
	}
}

func cloneScreenShareEvent(event *pb.ScreenShareEvent) *pb.ScreenShareEvent {
	if event == nil {
		return nil
	}
	clone := *event
	if event.EncryptionKey != nil {
		clone.EncryptionKey = append([]byte(nil), event.EncryptionKey...)
	}
	return &clone
}
