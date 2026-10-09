package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestVoiceRefreshPreservesAddressChangeClock(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
	first := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	rebound := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40001}
	start := time.Unix(1000, 0)
	for _, step := range []struct {
		name    string
		counter uint64
		addr    *net.UDPAddr
		elapsed time.Duration
		want    bool
	}{
		{"initial", 1, first, 0, true},
		{"refresh", 2, first, voiceRebindInterval / 2, true},
		{"before boundary", 3, rebound, voiceRebindInterval - time.Nanosecond, false},
		{"boundary despite refresh", 3, rebound, voiceRebindInterval, true},
		{"replay cannot move", 3, first, 2 * voiceRebindInterval, false},
		{"refresh after rebind", 4, rebound, voiceRebindInterval + time.Second, true},
		{"backwards clock", 5, first, voiceRebindInterval - time.Second, false},
		{"next change before boundary", 5, first, 2*voiceRebindInterval - time.Nanosecond, false},
		{"next change boundary", 5, first, 2 * voiceRebindInterval, true},
	} {
		data, err := protocol.MarshalVoiceRegistration(session.ID, step.counter, session.VoiceRegistrationKey)
		if err != nil {
			t.Fatal(err)
		}
		if got := srv.handleVoiceRegistration(data, step.addr, start.Add(step.elapsed)); got != step.want {
			t.Fatalf("%s accepted=%v want=%v", step.name, got, step.want)
		}
	}
}
