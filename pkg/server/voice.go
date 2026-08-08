package server

import (
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

const voiceRebindInterval = 5 * time.Second

// StartVoice starts the UDP voice forwarder.
func (s *Server) StartVoice() error {
	if !s.beginTask() {
		return fmt.Errorf("server: start voice: %w", s.ctx.Err())
	}
	defer s.endTask()

	s.listenerMu.Lock()
	if s.voiceStarting || s.voiceConn != nil {
		s.listenerMu.Unlock()
		return fmt.Errorf("server: voice already started")
	}
	s.voiceStarting = true
	s.listenerMu.Unlock()
	defer func() {
		s.listenerMu.Lock()
		s.voiceStarting = false
		s.listenerMu.Unlock()
	}()

	addr, err := net.ResolveUDPAddr("udp", s.cfg.VoiceAddr)
	if err != nil {
		return fmt.Errorf("server: resolve voice addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("server: listen voice: %w", err)
	}
	s.listenerMu.Lock()
	s.voiceConn = conn
	s.listenerMu.Unlock()

	// Increase UDP buffer size for better performance
	if err := conn.SetReadBuffer(1024 * 1024); err != nil {
		slog.Warn("failed to set UDP read buffer", "err", err)
	}
	if err := conn.SetWriteBuffer(1024 * 1024); err != nil {
		slog.Warn("failed to set UDP write buffer", "err", err)
	}

	slog.Info("voice plane listening", "addr", s.cfg.VoiceAddr)

	if !s.startWorker(func() { s.voiceLoop(conn) }) {
		_ = conn.Close()
		return fmt.Errorf("server: start voice worker: %w", s.ctx.Err())
	}
	return nil
}

// voiceLoop reads UDP voice packets and forwards them to channel members.
// This is an SFU (Selective Forwarding Unit) - no decryption, no mixing.
func (s *Server) voiceLoop(conn *net.UDPConn) {
	buf := make([]byte, protocol.VoiceHeaderSize+protocol.MaxVoicePayload)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				slog.Error("voice read error", "err", err)
				continue
			}
		}

		if protocol.IsVoiceRegistration(buf[:n]) {
			if !s.handleVoiceRegistration(buf[:n], remoteAddr, time.Now()) {
				s.metrics.VoicePacketsDropped.Add(1)
			}
			continue
		}

		if n < protocol.VoiceHeaderSize {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // too short, discard
		}

		s.metrics.VoicePacketsIn.Add(1)
		s.metrics.VoiceBytesIn.Add(int64(n))

		pkt, err := protocol.UnmarshalVoicePacket(buf[:n])
		if err != nil {
			s.metrics.VoicePacketsDropped.Add(1)
			continue
		}

		// Look up sender session
		session, ok := s.sessions.GetSnapshot(pkt.SessionID)
		if !ok {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // unknown session, discard
		}

		// Voice is accepted only after a control-authenticated registration proof.
		if !udpAddrEqual(session.UDPAddr, remoteAddr) {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // unregistered or mismatched source
		}

		// Don't forward if muted
		if session.Muted {
			s.metrics.VoicePacketsDropped.Add(1)
			continue
		}

		// Verify the sender is actually in the claimed channel (prevent channel spoofing)
		actualChannel := s.channels.ChannelOf(pkt.SessionID)
		if actualChannel <= 0 {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // not in a channel, discard
		}
		packetChannel := uint64(actualChannel) //nolint:gosec // positivity is checked immediately above
		if packetChannel != pkt.ChannelID {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // claimed channel does not match membership
		}

		// Track per-session voice debug stats
		var stat *perSessionVoiceStat
		if s.voiceDebugEnabled {
			stat = s.getOrCreateStat(pkt.SessionID)
			stat.packetsReceived.Add(1)
			stat.lastActivity.Store(time.Now().UnixNano())
		}

		channelID := actualChannel
		members := s.channels.Members(channelID)

		rawPacket := buf[:n] // forward raw bytes, no decryption

		for _, memberSID := range members {
			if memberSID == pkt.SessionID {
				continue // don't echo back to sender
			}

			memberSession, ok := s.sessions.GetSnapshot(memberSID)
			if !ok || memberSession.UDPAddr == nil {
				continue
			}
			if memberSession.Deafened {
				continue // don't send to deafened users
			}

			_, err := conn.WriteToUDP(rawPacket, memberSession.UDPAddr)
			if err != nil {
				slog.Debug("voice forward error", "target", memberSID, "err", err)
			} else {
				s.metrics.VoicePacketsOut.Add(1)
				s.metrics.VoiceBytesOut.Add(int64(n))
				if stat != nil {
					stat.packetsForwarded.Add(1)
				}
			}
		}
	}
}

func (s *Server) handleVoiceRegistration(data []byte, remoteAddr *net.UDPAddr, now time.Time) bool {
	registration, err := protocol.UnmarshalVoiceRegistration(data)
	if err != nil {
		return false
	}
	return s.sessions.RegisterUDPAddr(registration.SessionID, registration, remoteAddr, now, voiceRebindInterval)
}
