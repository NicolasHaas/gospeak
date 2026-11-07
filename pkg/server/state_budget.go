package server

import (
	"fmt"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func (s *Server) validateStateConfig() error {
	if s.cfg.MaxSessions > model.MaxServerSessions {
		return fmt.Errorf("server: max sessions exceeds supported wire-state limit %d", model.MaxServerSessions)
	}
	// A DNS name plus port fits comfortably; cap bytes before JSON escaping.
	if s.cfg.EnableScreenShare && len(s.cfg.ScreenAddr) > 256 {
		return fmt.Errorf("server: screen address exceeds 256 bytes")
	}
	return nil
}

func validateChannelState(channels []model.Channel) error {
	remaining := model.MaxChannelStateBytes
	for _, ch := range channels {
		if err := ch.Validate(); err != nil {
			return fmt.Errorf("server: invalid persisted channel %d: %w", ch.ID, err)
		}
		remaining -= model.ChannelStateBytes(ch)
		if remaining < 0 {
			return fmt.Errorf("server: persisted channels exceed serialized state budget %d; restore a backup or shorten metadata offline before restarting", model.MaxChannelStateBytes)
		}
	}
	return nil
}
