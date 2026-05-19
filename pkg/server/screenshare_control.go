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
	s.metrics.ScreenSharesStarted.Add(1)
	slog.Info("screen share started", "user", session.Username, "session", sessionID, "channel", session.ChannelID, "width", req.Width, "height", req.Height)

	handler.sendToSession(sessionID, &pb.ControlMessage{ScreenShareEvent: event})
	publicEvent, _ := s.screenShare.PublicEvent(session.ChannelID)
	handler.broadcastToChannel(session.ChannelID, &pb.ControlMessage{ScreenShareEvent: publicEvent}, 0)
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
	if event.SessionID == sessionID {
		s.screenShare.Unsubscribe(sessionID)
		sendError(conn, 40, "cannot subscribe to your own share")
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
