package server

import (
	"log/slog"
	"net"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const minScreenShareFrameInterval = 500 * time.Millisecond

func (s *Server) handleScreenShareStart(handler *ControlHandler, sessionID uint32, req *pb.ScreenShareStartRequest, conn net.Conn) {
	if !s.cfg.EnableScreenShare {
		sendError(conn, 40, "screen sharing is disabled on this server")
		return
	}

	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if session.ChannelID == 0 {
		sendError(conn, 40, "join a channel before sharing your screen")
		return
	}
	if req.Width <= 0 || req.Height <= 0 {
		sendError(conn, 40, "invalid screen dimensions")
		return
	}

	event, err := s.screenShare.Start(session.ChannelID, sessionID, session.UserID, session.Username, req.Width, req.Height)
	if err != nil {
		sendError(conn, 40, err.Error())
		return
	}
	viewerSessionIDs := s.channels.Members(session.ChannelID)
	_, sharedSessionIDs, err := s.screenShare.ShareWithViewers(sessionID, viewerSessionIDs)
	if err != nil {
		sendError(conn, 40, err.Error())
		return
	}
	for _, viewerSessionID := range sharedSessionIDs {
		handler.sendToSession(viewerSessionID, &pb.ControlMessage{ScreenShareEvent: event})
	}
	s.metrics.ScreenSharesStarted.Add(1)
	slog.Info("screen share started", "user", session.Username, "session", sessionID, "channel", session.ChannelID, "width", req.Width, "height", req.Height)

	handler.sendToSession(sessionID, &pb.ControlMessage{ScreenShareEvent: event})
	publicEvent, _ := s.screenShare.PublicEvent(session.ChannelID)
	handler.broadcastToChannel(session.ChannelID, &pb.ControlMessage{ScreenShareEvent: publicEvent}, 0)
}

func (s *Server) handleScreenShareShare(handler *ControlHandler, sessionID uint32, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if session.ChannelID == 0 {
		sendError(conn, 40, "join a channel before sharing your screen")
		return
	}
	if !s.screenShare.IsSharer(sessionID, session.ChannelID) {
		sendError(conn, 40, "only the active sharer can share screen access")
		return
	}

	event, sharedSessionIDs, err := s.screenShare.ShareWithViewers(sessionID, s.channels.Members(session.ChannelID))
	if err != nil {
		sendError(conn, 40, err.Error())
		return
	}
	for _, viewerSessionID := range sharedSessionIDs {
		handler.sendToSession(viewerSessionID, &pb.ControlMessage{ScreenShareEvent: event})
	}
	if len(sharedSessionIDs) > 0 {
		slog.Info("screen share shared with channel", "sharer_session", sessionID, "channel", session.ChannelID, "viewer_count", len(sharedSessionIDs))
	}
}

func (s *Server) handleScreenShareStop(handler *ControlHandler, sessionID uint32) {
	session, _ := s.sessions.GetSnapshot(sessionID)
	event, ok := s.screenShare.StopBySession(sessionID)
	if !ok || event == nil {
		return
	}
	s.metrics.ScreenSharesStopped.Add(1)
	s.metrics.ScreenShareSubscribers.Store(s.screenShare.SubscriberCount())
	slog.Info("screen share stopped", "user", session.Username, "session", sessionID, "channel", event.ChannelID)
	handler.broadcastToChannel(event.ChannelID, &pb.ControlMessage{ScreenShareEvent: event}, 0)
}

func (s *Server) handleScreenShareSubscribe(handler *ControlHandler, sessionID uint32, req *pb.ScreenShareSubscribeRequest, conn net.Conn) {
	if !s.cfg.EnableScreenShare {
		sendError(conn, 40, "screen sharing is disabled on this server")
		return
	}

	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if session.ChannelID == 0 || session.ChannelID != req.ChannelID {
		sendError(conn, 40, "you can only watch shares in your current channel")
		return
	}

	event, err := s.screenShare.Subscribe(req.ChannelID, sessionID)
	if err != nil {
		sendError(conn, 40, err.Error())
		return
	}
	s.metrics.ScreenShareSubscribers.Store(s.screenShare.SubscriberCount())
	slog.Info("screen share subscribed", "viewer_session", sessionID, "sharer_session", event.SessionID, "channel", req.ChannelID)

	handler.sendToSession(sessionID, &pb.ControlMessage{ScreenShareEvent: event})
	publicEvent, _ := s.screenShare.PublicEvent(req.ChannelID)
	handler.broadcastToChannel(req.ChannelID, &pb.ControlMessage{ScreenShareEvent: publicEvent}, 0)
}

func (s *Server) handleScreenShareUnsubscribe(sessionID uint32) {
	s.screenShare.Unsubscribe(sessionID)
	s.metrics.ScreenShareSubscribers.Store(s.screenShare.SubscriberCount())
}
