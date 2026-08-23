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

// voiceLoop authenticates UDP voice packets and forwards their original
// ciphertext to channel members. It never decodes or mixes media plaintext.
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

		actualChannel, ok := s.acceptVoicePacket(pkt, remoteAddr)
		if !ok {
			s.metrics.VoicePacketsDropped.Add(1)
			continue
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

		rawPacket := buf[:n] // forward the original authenticated ciphertext unchanged

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

// acceptVoicePacket verifies endpoint ownership and packet authenticity before
// recording its sequence. It returns the sender's current channel when the
// packet is safe to relay.
func (s *Server) acceptVoicePacket(pkt *protocol.VoicePacket, remoteAddr *net.UDPAddr) (int64, bool) {
	if pkt == nil || s.voiceCipher == nil {
		return 0, false
	}
	session, ok := s.sessions.GetSnapshot(pkt.SessionID)
	if !ok || session.Muted || session.ChannelID <= 0 || !udpAddrEqual(session.UDPAddr, remoteAddr) {
		return 0, false
	}
	packetChannel := uint64(session.ChannelID) //nolint:gosec // positivity is checked immediately above
	if packetChannel != pkt.ChannelID {
		return 0, false
	}
	if _, err := s.voiceCipher.Decrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), pkt.Payload); err != nil {
		return 0, false
	}
	if hook := s.voiceReplayHook; hook != nil {
		hook()
	}
	acceptedSession, ok := s.sessions.AcceptVoiceSequence(pkt.SessionID, remoteAddr, pkt.SeqNum)
	if !ok {
		return 0, false
	}

	actualChannel := s.channels.ChannelOf(pkt.SessionID)
	if actualChannel <= 0 || actualChannel != acceptedSession.ChannelID {
		return 0, false
	}
	packetChannel = uint64(actualChannel) //nolint:gosec // positivity is checked immediately above
	if packetChannel != pkt.ChannelID {
		return 0, false
	}
	return actualChannel, true
}

func (s *Server) handleVoiceRegistration(data []byte, remoteAddr *net.UDPAddr, now time.Time) bool {
	registration, err := protocol.UnmarshalVoiceRegistration(data)
	if err != nil {
		return false
	}
	return s.sessions.RegisterUDPAddr(registration.SessionID, registration, remoteAddr, now, voiceRebindInterval)
}
