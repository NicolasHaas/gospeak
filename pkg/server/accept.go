package server

import (
	"errors"
	"log/slog"
	"net"
	"time"
)

// acceptConn backs off persistent listener errors without delaying shutdown.
// A successful accept resets the net/http-style 5ms-to-1s retry delay.
func (s *Server) acceptConn(ln net.Listener, plane preAuthPlane) (net.Conn, error) {
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err == nil || errors.Is(err, net.ErrClosed) {
			return conn, err
		}
		if s.ctx.Err() != nil {
			return nil, s.ctx.Err()
		}
		if delay == 0 {
			delay = 5 * time.Millisecond
		} else {
			delay = min(2*delay, time.Second)
		}
		slog.Error("accept error", "plane", plane, "err", err, "retry", delay)
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return nil, s.ctx.Err()
		case <-timer.C:
		}
	}
}
