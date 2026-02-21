package server

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// StartVoice starts the UDP voice forwarder.
func (s *Server) StartVoice() error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.VoiceAddr)
	if err != nil {
		return fmt.Errorf("server: resolve voice addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("server: listen voice: %w", err)
	}
	s.voiceConn = conn

	// Increase UDP buffer size for better performance
	if err := conn.SetReadBuffer(1024 * 1024); err != nil {
		slog.Warn("failed to set UDP read buffer", "err", err)
	}
	if err := conn.SetWriteBuffer(1024 * 1024); err != nil {
		slog.Warn("failed to set UDP write buffer", "err", err)
	}

	slog.Info("voice plane listening", "addr", s.cfg.VoiceAddr)

	go s.voiceLoop()
	return nil
}

// voiceLoop reads UDP voice packets and forwards them to channel members.
// This is an SFU (Selective Forwarding Unit) - no decryption, no mixing.
func (s *Server) voiceLoop() {
	buf := make([]byte, protocol.VoiceHeaderSize+protocol.MaxVoicePayload)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		n, remoteAddr, err := s.voiceConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				slog.Error("voice read error", "err", err)
				continue
			}
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

		// Verify UDP source matches registered address (prevent session hijacking).
		// Only the first packet from a session registers the address; subsequent
		// packets from different sources are rejected.
		if session.UDPAddr == nil {
			s.sessions.SetUDPAddr(pkt.SessionID, remoteAddr)
		} else if !session.UDPAddr.IP.Equal(remoteAddr.IP) || session.UDPAddr.Port != remoteAddr.Port {
			continue // source mismatch, drop (prevents UDP session hijack)
		}

		// Don't forward if muted
		if session.Muted {
			s.metrics.VoicePacketsDropped.Add(1)
			continue
		}

		// Verify the sender is actually in the claimed channel (prevent channel spoofing)
		actualChannel := s.channels.ChannelOf(pkt.SessionID)
		if actualChannel == 0 || actualChannel != int64(pkt.ChannelID) {
			s.metrics.VoicePacketsDropped.Add(1)
			continue // not in this channel, discard
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

			_, err := s.voiceConn.WriteToUDP(rawPacket, memberSession.UDPAddr)
			if err != nil {
				slog.Debug("voice forward error", "target", memberSID, "err", err)
			} else {
				s.metrics.VoicePacketsOut.Add(1)
				s.metrics.VoiceBytesOut.Add(int64(n))
			}
		}
	}
}
