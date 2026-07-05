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
	authorizedByShare  map[uint32]map[uint32]bool
	subscribersByShare map[uint32]map[uint32]bool
	authorizedTarget   map[uint32]uint32
	viewerTarget       map[uint32]uint32
	lastFrameAt        map[uint32]time.Time
}

func NewScreenShareManager() *ScreenShareManager {
	return &ScreenShareManager{
		activeByChannel:    make(map[int64]*pb.ScreenShareEvent),
		channelBySession:   make(map[uint32]int64),
		authorizedByShare:  make(map[uint32]map[uint32]bool),
		subscribersByShare: make(map[uint32]map[uint32]bool),
		authorizedTarget:   make(map[uint32]uint32),
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
	if _, ok := m.authorizedByShare[sessionID]; !ok {
		m.authorizedByShare[sessionID] = make(map[uint32]bool)
	}
	if _, ok := m.subscribersByShare[sessionID]; !ok {
		m.subscribersByShare[sessionID] = make(map[uint32]bool)
	}
	m.lastFrameAt[sessionID] = time.Now()
	return cloneScreenShareEvent(event), nil
}

func (m *ScreenShareManager) StopBySession(sessionID uint32) (*pb.ScreenShareEvent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopBySessionLocked(sessionID)

}

func (m *ScreenShareManager) ExpireInactive(idleTimeout time.Duration) []*pb.ScreenShareEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	if idleTimeout <= 0 {
		return nil
	}

	now := time.Now()
	expired := make([]*pb.ScreenShareEvent, 0)
	for sessionID := range m.channelBySession {
		last := m.lastFrameAt[sessionID]
		if last.IsZero() || now.Sub(last) <= idleTimeout {
			continue
		}
		stopped, ok := m.stopBySessionLocked(sessionID)
		if ok && stopped != nil {
			expired = append(expired, stopped)
		}
	}
	return expired
}

func (m *ScreenShareManager) stopBySessionLocked(sessionID uint32) (*pb.ScreenShareEvent, bool) {

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
	for viewer := range m.authorizedByShare[sessionID] {
		delete(m.authorizedTarget, viewer)
	}
	for viewer := range m.subscribersByShare[sessionID] {
		delete(m.viewerTarget, viewer)
	}
	delete(m.authorizedByShare, sessionID)
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
	if active.SessionID == viewerSessionID {
		return nil, fmt.Errorf("cannot subscribe to your own share")
	}
	if m.authorizedTarget[viewerSessionID] != active.SessionID {
		return nil, fmt.Errorf("screen share is not shared with you")
	}

	m.removeSubscriberLocked(viewerSessionID)
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
	clone := cloneScreenShareEvent(active)
	clone.EncryptionKey = nil
	return clone, nil
}

func (m *ScreenShareManager) ShareWithViewers(sharerSessionID uint32, viewerSessionIDs []uint32) (*pb.ScreenShareEvent, []uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	channelID, ok := m.channelBySession[sharerSessionID]
	if !ok {
		return nil, nil, fmt.Errorf("screen share is no longer active")
	}
	active, ok := m.activeByChannel[channelID]
	if !ok || !active.Active || active.SessionID != sharerSessionID {
		return nil, nil, fmt.Errorf("screen share is no longer active")
	}
	if _, ok := m.authorizedByShare[sharerSessionID]; !ok {
		m.authorizedByShare[sharerSessionID] = make(map[uint32]bool)
	}

	shared := make([]uint32, 0, len(viewerSessionIDs))
	for _, viewerSessionID := range viewerSessionIDs {
		if viewerSessionID == sharerSessionID {
			continue
		}
		if m.authorizedTarget[viewerSessionID] == sharerSessionID {
			continue
		}
		m.authorizedByShare[sharerSessionID][viewerSessionID] = true
		m.authorizedTarget[viewerSessionID] = sharerSessionID
		shared = append(shared, viewerSessionID)
	}
	return cloneScreenShareEvent(active), shared, nil
}

func (m *ScreenShareManager) Unsubscribe(viewerSessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeSubscriberLocked(viewerSessionID)
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
	m.removeSubscriberLocked(viewerSessionID)
	m.removeAuthorizationLocked(viewerSessionID)
}

func (m *ScreenShareManager) removeSubscriberLocked(viewerSessionID uint32) {
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

func (m *ScreenShareManager) removeAuthorizationLocked(viewerSessionID uint32) {
	sharerSessionID, ok := m.authorizedTarget[viewerSessionID]
	if !ok {
		return
	}
	delete(m.authorizedTarget, viewerSessionID)
	if viewers, ok := m.authorizedByShare[sharerSessionID]; ok {
		delete(viewers, viewerSessionID)
		if len(viewers) == 0 {
			delete(m.authorizedByShare, sharerSessionID)
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
