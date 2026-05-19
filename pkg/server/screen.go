package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// StartScreen starts the TCP/TLS screen-share relay listener.
func (s *Server) StartScreen() error {
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
	s.screenConn = ln
	slog.Info("screen plane listening", "addr", s.cfg.ScreenAddr)

	go func() {
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
			go s.handleScreenConn(conn)
		}
	}()

	return nil
}

func (s *Server) handleScreenConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	auth, err := protocol.ReadScreenAuth(conn)
	if err != nil {
		slog.Error("screen auth read failed", "err", err)
		return
	}
	if !s.sessions.ValidateScreenAuth(auth.SessionID, auth.Token) {
		slog.Warn("screen auth rejected", "session", auth.SessionID)
		return
	}

	s.setScreenConn(auth.SessionID, conn)
	defer s.removeScreenConn(auth.SessionID, conn)

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
	s.metrics.ScreenShareFramesIn.Add(1)
	s.metrics.ScreenShareBytesIn.Add(int64(len(pkt.Payload)))

	viewers := s.screenShare.SubscribersForSharer(sessionID)
	forwarded := 0
	for _, viewerSessionID := range viewers {
		if s.sendScreenPacketToSession(viewerSessionID, pkt) {
			forwarded++
		}
	}
	s.metrics.ScreenShareFramesOut.Add(int64(forwarded))
	s.metrics.ScreenShareBytesOut.Add(int64(forwarded * len(pkt.Payload)))
}
