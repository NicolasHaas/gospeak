package client

import (
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const controlHeartbeatInterval = time.Minute

func (g *connectionGeneration) controlHeartbeat(control *ControlClient, ticks <-chan time.Time) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case now := <-ticks:
			if g.ctx.Err() != nil {
				return
			}
			if err := control.Send(&pb.ControlMessage{Ping: &pb.Ping{Timestamp: now.UnixMilli()}}); err != nil {
				_ = control.Close()
				return // The tracked receiver owns disconnect notification.
			}
		}
	}
}
