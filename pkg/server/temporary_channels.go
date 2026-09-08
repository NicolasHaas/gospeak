package server

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const (
	tempChannelCreationCooldown = 10 * time.Second
	tempChannelEmptyGrace       = 5 * time.Minute
	tempChannelSweepRetry       = 30 * time.Second
	tempChannelIdleSweep        = time.Hour
	maxTempChannelsPerUser      = 5
	maxOpenTempChannelsPerUser  = 2
	maxTempChannelsPerParent    = 8
	maxTempChannels             = 64
)

var (
	errTempChannelParentMembership = errors.New("temporary channel requires parent membership")
	errTempChannelScope            = errors.New("temporary channel parent is outside session scope")
	errTempChannelCooldown         = errors.New("temporary channel creation cooldown active")
	errTempChannelUserQuota        = errors.New("temporary channel user quota reached")
	errTempChannelParentQuota      = errors.New("temporary channel parent quota reached")
	errTempChannelGlobalQuota      = errors.New("temporary channel server quota reached")
	errTempChannelUnavailable      = errors.New("temporary channel unavailable")
)

type tempChannelLifecycle struct {
	mu           sync.Mutex
	now          func() time.Time
	maxPerUser   int
	lastCreation map[int64]time.Time
	emptySince   map[int64]time.Time
	wake         chan struct{}
}

func newTempChannelLifecycle(openServer bool) *tempChannelLifecycle {
	maxPerUser := maxTempChannelsPerUser
	if openServer {
		maxPerUser = maxOpenTempChannelsPerUser
	}
	return &tempChannelLifecycle{
		now:          time.Now,
		maxPerUser:   maxPerUser,
		lastCreation: make(map[int64]time.Time),
		emptySince:   make(map[int64]time.Time),
		wake:         make(chan struct{}, 1),
	}
}

func (l *tempChannelLifecycle) create(
	session SessionSnapshot,
	request *pb.CreateChannelRequest,
	channelManager *ChannelManager,
	store datastore.DataProviderFactory,
	name, description string,
) (*model.Channel, error) {
	if channelManager.ChannelOf(session.ID) != request.ParentID {
		return nil, errTempChannelParentMembership
	}
	if session.ChannelScope != 0 && session.ChannelScope != request.ParentID {
		return nil, errTempChannelScope
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for userID, last := range l.lastCreation {
		if now.Sub(last) >= tempChannelCreationCooldown {
			delete(l.lastCreation, userID)
		}
	}
	if last, ok := l.lastCreation[session.UserID]; ok && now.Sub(last) < tempChannelCreationCooldown {
		return nil, errTempChannelCooldown
	}

	channels, err := store.NonTx().ListChannels()
	if err != nil {
		return nil, fmt.Errorf("list temporary channels: %w", err)
	}
	var userCount, parentCount, totalCount int
	parentAllowsSubChannels := false
	for i := range channels {
		channel := &channels[i]
		if channel.ID == request.ParentID {
			parentAllowsSubChannels = channel.AllowSubChannels
		}
		if !channel.IsTemp {
			continue
		}
		totalCount++
		if channel.ParentID == request.ParentID {
			parentCount++
		}
		if channel.CreatedBy == session.UserID {
			userCount++
		}
	}
	switch {
	case !parentAllowsSubChannels:
		return nil, errTempChannelUnavailable
	case userCount >= l.maxPerUser:
		return nil, errTempChannelUserQuota
	case parentCount >= maxTempChannelsPerParent:
		return nil, errTempChannelParentQuota
	case totalCount >= maxTempChannels:
		return nil, errTempChannelGlobalQuota
	}

	channel := &model.Channel{
		Name:        name,
		Description: description,
		MaxUsers:    int(request.MaxUsers),
		ParentID:    request.ParentID,
		IsTemp:      true,
		CreatedBy:   session.UserID,
	}
	if err := store.NonTx().CreateChannel(channel); err != nil {
		return nil, fmt.Errorf("create temporary channel: %w", err)
	}
	l.lastCreation[session.UserID] = now
	l.emptySince[channel.ID] = now
	l.notifyLocked()
	return channel, nil
}

func (l *tempChannelLifecycle) tryJoin(channel *model.Channel, sessionID uint32, channels *ChannelManager, sessions *SessionManager, store datastore.DataProviderFactory) (int64, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, err := store.NonTx().GetChannel(channel.ID)
	if err != nil {
		return channels.ChannelOf(sessionID), false, fmt.Errorf("recheck channel: %w", err)
	}
	if current == nil {
		return channels.ChannelOf(sessionID), false, errTempChannelUnavailable
	}
	previous, joined := channels.TryJoin(sessionID, current.ID, current.MaxUsers)
	if joined {
		sessions.SetChannel(sessionID, current.ID)
	}
	if joined && current.IsTemp {
		delete(l.emptySince, channel.ID)
		l.notifyLocked()
	}
	return previous, joined, nil
}

func (l *tempChannelLifecycle) markEmpty(channelID int64) {
	l.mu.Lock()
	if _, tracked := l.emptySince[channelID]; !tracked {
		l.emptySince[channelID] = l.now()
		l.notifyLocked()
	}
	l.mu.Unlock()
}

func (l *tempChannelLifecycle) delete(store datastore.DataProviderFactory, channelID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := store.NonTx().DeleteChannel(channelID); err != nil {
		return err
	}
	delete(l.emptySince, channelID)
	l.notifyLocked()
	return nil
}

func (l *tempChannelLifecycle) notifyLocked() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (s *Server) runTempChannelJanitor(handler *ControlHandler, store datastore.DataProviderFactory) {
	timer := time.NewTimer(s.sweepTempChannels(handler, store))
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.tempChannels.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		timer.Reset(s.sweepTempChannels(handler, store))
	}
}

func (s *Server) sweepTempChannels(handler *ControlHandler, store datastore.DataProviderFactory) time.Duration {
	lifecycle := s.tempChannels
	lifecycle.mu.Lock()
	now := lifecycle.now()
	channels, err := store.NonTx().ListChannels()
	if err != nil {
		lifecycle.mu.Unlock()
		slog.Error("list temporary channels for cleanup", "err", err)
		return tempChannelSweepRetry
	}

	present := make(map[int64]bool, len(channels))
	for i := range channels {
		present[channels[i].ID] = true
	}
	for channelID := range lifecycle.emptySince {
		if !present[channelID] {
			delete(lifecycle.emptySince, channelID)
		}
	}
	expired := make([]model.Channel, 0)
	var nextDeadline time.Time
	deleteFailed := false
	for i := range channels {
		channel := &channels[i]
		if !channel.IsTemp {
			continue
		}

		empty := s.channels.MembersCount(channel.ID) == 0
		if empty {
			if _, tracked := lifecycle.emptySince[channel.ID]; !tracked {
				lifecycle.emptySince[channel.ID] = now
			}
		} else {
			delete(lifecycle.emptySince, channel.ID)
		}
		emptySince := lifecycle.emptySince[channel.ID]
		emptyExpired := empty && !emptySince.IsZero() && !now.Before(emptySince.Add(tempChannelEmptyGrace))
		orphaned := channel.ParentID <= 0 || !present[channel.ParentID]
		if !emptyExpired && !orphaned {
			if empty && !emptySince.IsZero() {
				deadline := emptySince.Add(tempChannelEmptyGrace)
				if nextDeadline.IsZero() || deadline.Before(nextDeadline) {
					nextDeadline = deadline
				}
			}
			continue
		}
		if err := store.NonTx().DeleteChannel(channel.ID); err != nil {
			slog.Error("delete expired temporary channel", "id", channel.ID, "err", err)
			deleteFailed = true
			continue
		}
		delete(lifecycle.emptySince, channel.ID)
		delete(present, channel.ID)
		expired = append(expired, *channel)
	}
	lifecycle.mu.Unlock()
	if deleteFailed {
		retryDeadline := now.Add(tempChannelSweepRetry)
		if nextDeadline.IsZero() || retryDeadline.Before(nextDeadline) {
			nextDeadline = retryDeadline
		}
	}

	if len(expired) == 0 {
		return tempChannelDelay(now, nextDeadline)
	}
	for i := range expired {
		channel := &expired[i]
		s.evictChannelMembers(channel.ID, handler)
		s.metrics.ChannelsDeleted.Add(1)
		slog.Info("temporary channel expired", "id", channel.ID, "name", channel.Name)
	}
	if handler != nil {
		s.broadcastServerState(store, handler)
	}
	return tempChannelDelay(now, nextDeadline)
}

func (s *Server) evictChannelMembers(channelID int64, handler *ControlHandler) {
	for _, sessionID := range s.channels.Members(channelID) {
		s.evictChannelMemberIfCurrent(channelID, sessionID, handler)
	}
	s.metrics.ScreenShareSubscribers.Store(s.screenShare.SubscriberCount())
}

func (s *Server) evictChannelMemberIfCurrent(channelID int64, sessionID uint32, handler *ControlHandler) bool {
	if !s.channels.LeaveIf(sessionID, channelID) {
		return false
	}
	if event, stopped := s.screenShare.LeaveChannel(sessionID, channelID); stopped && event != nil {
		s.metrics.ScreenSharesStopped.Add(1)
		if handler != nil {
			handler.broadcastToChannel(channelID, &pb.ControlMessage{ScreenShareEvent: event}, sessionID)
		}
	}
	s.sessions.ClearChannelIf(sessionID, channelID)
	return true
}

func tempChannelDelay(now, deadline time.Time) time.Duration {
	if deadline.IsZero() {
		return tempChannelIdleSweep
	}
	delay := deadline.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}
