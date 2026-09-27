package server

import (
	"log/slog"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const chatPageLimit = 40 // JSON can escape each of 2000 runes as six bytes; 40 messages fit 512 KiB.

func (s *Server) chatChannel(sessionID uint32, channelID int64, st datastore.DataProviderFactory) bool {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok || channelID <= 0 || session.ChannelScope != 0 && session.ChannelScope != channelID {
		return false
	}
	channel, err := st.NonTx().GetChannel(channelID)
	return err == nil && channel != nil
}

func (s *Server) handleChatMessage(handler *ControlHandler, sessionID uint32, chat *pb.ChatMessage, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		return
	}
	channelID := chat.ChannelID
	if channelID == 0 {
		channelID = s.channels.ChannelOf(sessionID)
	} // old clients use their voice channel
	if !s.chatChannel(sessionID, channelID, st) {
		sendError(conn, 3, "channel not found or inaccessible")
		return
	}
	text := sanitizeText(strings.TrimSpace(chat.Text))
	if text == "" || utf8.RuneCountInString(text) > model.MessageMaxBodyLength {
		return
	}
	message := &model.Message{ChannelID: channelID, SenderID: session.UserID, SenderName: session.Username, Body: text}
	if err := st.NonTx().CreateMessageWithRetention(message, s.cfg.ChatHistoryLimit, s.cfg.ChatMaxAge); err != nil {
		slog.Error("store chat message", "err", err)
		sendError(conn, 3, "could not save message")
		return
	}
	handler.broadcastChat(channelID, &pb.ControlMessage{ChatEvent: &pb.ChatMessage{
		ID: message.ID, ChannelID: channelID, SenderID: session.UserID, SenderName: session.Username,
		Text: text, Timestamp: message.CreatedAt.Unix(),
	}})
	s.metrics.ChatMessagesSent.Add(1)
}

func (s *Server) handleChatHistory(handler *ControlHandler, sessionID uint32, req *pb.ChatHistoryRequest, st datastore.DataProviderFactory, conn net.Conn) {
	if req.BeforeID < 0 || req.Limit < 0 || req.Limit > chatPageLimit || !s.chatChannel(sessionID, req.ChannelID, st) {
		sendError(conn, 3, "channel not found or inaccessible")
		return
	}
	// Select before reading history so a concurrent write reaches either the page or live fanout.
	// A message can appear in both; clients should deduplicate by its stored ID.
	handler.mu.Lock()
	if _, registered := handler.connMap[sessionID]; registered {
		handler.chatSelection[sessionID] = req.ChannelID
	}
	handler.mu.Unlock()
	limit := req.Limit
	if limit == 0 {
		limit = chatPageLimit
	}
	fetch := limit + 1
	filters := model.MessageFilters{LimitToChannelID: &req.ChannelID, BeforeID: req.BeforeID, PageSize: &fetch}
	if s.cfg.ChatMaxAge > 0 {
		filters.Since = time.Now().Add(-s.cfg.ChatMaxAge)
	}
	rows, err := st.NonTx().ListMessages(filters)
	if err != nil {
		slog.Error("load chat history", "err", err)
		sendError(conn, 3, "could not load history")
		return
	}
	resp := &pb.ChatHistoryResponse{ChannelID: req.ChannelID, HasMore: int64(len(rows)) > limit}
	if resp.HasMore {
		rows = rows[:limit]
	}
	// ponytail: cap legacy rows at current wire limits; stored originals remain untouched.
	for _, m := range rows {
		resp.Messages = append(resp.Messages, pb.ChatMessage{ID: m.ID, ChannelID: m.ChannelID,
			SenderID: m.SenderID, SenderName: truncateRunes(m.SenderName, model.MaxUsernameLength),
			Text: truncateRunes(m.Body, model.MessageMaxBodyLength), Timestamp: m.CreatedAt.Unix()})
	}
	if err := writeControlMessage(conn, &pb.ControlMessage{ChatHistoryResp: resp}); err != nil {
		slog.Warn("send chat history", "session", sessionID, "err", err)
	}
}

func (handler *ControlHandler) broadcastChat(channelID int64, event *pb.ControlMessage) {
	members := handler.server.channels.Members(channelID)
	legacy := make(map[uint32]bool, len(members))
	for _, sessionID := range members {
		legacy[sessionID] = true
	}
	handler.mu.RLock()
	clients := make(map[uint32]*controlClient)
	for sessionID, client := range handler.connMap {
		selected, explicit := handler.chatSelection[sessionID]
		if explicit && selected == channelID || !explicit && legacy[sessionID] {
			// Membership and explicit selection both remain subject to the account's scope.
			if session, ok := handler.server.sessions.GetSnapshot(sessionID); ok &&
				(session.ChannelScope == 0 || session.ChannelScope == channelID) {
				clients[sessionID] = client
			}
		}
	}
	handler.mu.RUnlock()
	for sessionID, client := range clients {
		if err := client.send(event); err != nil {
			slog.Error("chat event write failed", "session", sessionID, "err", err)
		}
	}
}

// ponytail: one bounded batch per minute; old imported backlogs drain over multiple ticks.
func (s *Server) runChatJanitor(st datastore.DataProviderFactory) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if _, err := st.NonTx().PruneExpiredMessages(s.cfg.ChatMaxAge, 1000); err != nil {
				slog.Error("prune expired chat", "err", err)
			}
		}
	}
}
