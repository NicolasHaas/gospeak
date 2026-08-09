package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// StartScreen starts the TCP/TLS screen-share relay listener.
func (s *Server) StartScreen() error {
	if !s.beginTask() {
		return fmt.Errorf("server: start screen: %w", s.ctx.Err())
	}
	defer s.endTask()

	s.listenerMu.Lock()
	if s.screenStarting || s.screenConn != nil {
		s.listenerMu.Unlock()
		return fmt.Errorf("server: screen already started")
	}
	s.screenStarting = true
	s.listenerMu.Unlock()
	defer func() {
		s.listenerMu.Lock()
		s.screenStarting = false
		s.listenerMu.Unlock()
	}()

	cert, err := loadOrGenerateTLS(s.cfg)
	if err != nil {
		return fmt.Errorf("server: screen tls: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	ln, err := tls.Listen("tcp", s.cfg.ScreenAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("server: listen screen: %w", err)
	}
	s.listenerMu.Lock()
	s.screenConn = ln
	s.listenerMu.Unlock()
	slog.Info("screen plane listening", "addr", s.cfg.ScreenAddr)

	if !s.startWorker(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					slog.Error("screen accept error", "err", err)
					continue
				}
			}
			if !s.beginPreAuth(conn, preAuthScreen) {
				slog.Warn("screen pre-auth connection limit reached", "remote", conn.RemoteAddr())
				_ = conn.Close()
				continue
			}
			acceptedConn := conn
			if !s.startWorker(func() { s.handleScreenConn(acceptedConn) }) {
				s.forgetAcceptedConn(conn)
				_ = conn.Close()
			}
		}
	}) {
		_ = ln.Close()
		return fmt.Errorf("server: start screen worker: %w", s.ctx.Err())
	}

	return nil
}

func (s *Server) handleScreenConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if !s.beginPreAuth(conn, preAuthScreen) {
		return
	}
	defer s.forgetAcceptedConn(conn)
	if err := conn.SetDeadline(time.Now().Add(s.cfg.PreAuthTimeout)); err != nil {
		slog.Error("set screen auth deadline", "err", err)
		return
	}

	auth, err := protocol.ReadScreenAuth(conn)
	if err != nil {
		slog.Error("screen auth read failed", "err", err)
		return
	}
	if !s.sessions.ValidateScreenAuth(auth.SessionID, auth.Token) {
		slog.Warn("screen auth rejected", "session", auth.SessionID)
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		slog.Error("clear screen auth deadline", "session", auth.SessionID, "err", err)
		return
	}
	s.finishPreAuth(conn)

	client := s.setScreenConn(auth.SessionID, conn)
	defer s.removeScreenConn(auth.SessionID, client)

	for {
		pkt, err := protocol.ReadScreenPacket(conn)
		if err != nil {
			if err == io.EOF || isClosedErr(err) {
				return
			}
			slog.Error("screen read error", "session", auth.SessionID, "err", err)
			return
		}
		s.handleScreenPacket(auth.SessionID, pkt)
	}
}

func (s *Server) handleScreenPacket(sessionID uint32, pkt *protocol.ScreenPacket) {
	if pkt == nil || len(pkt.Payload) == 0 {
		return
	}
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok || session.ChannelID == 0 {
		return
	}
	if !s.screenShare.IsSharer(sessionID, session.ChannelID) {
		return
	}
	if !s.screenShare.AllowFrame(sessionID, minScreenShareFrameInterval) {
		return
	}

	pkt.SessionID = sessionID
	frame, err := protocol.MarshalScreenPacketFrame(pkt)
	if err != nil {
		slog.Error("marshal screen packet", "session", sessionID, "err", err)
		return
	}
	s.metrics.ScreenShareFramesIn.Add(1)
	s.metrics.ScreenShareBytesIn.Add(int64(len(pkt.Payload)))

	viewers := s.screenShare.SubscribersForSharer(sessionID)
	forwarded := 0
	for _, viewerSessionID := range viewers {
		if s.sendScreenFrameToSession(viewerSessionID, frame) {
			forwarded++
		}
	}
	s.metrics.ScreenShareFramesOut.Add(int64(forwarded))
	s.metrics.ScreenShareBytesOut.Add(int64(forwarded * len(pkt.Payload)))
}
