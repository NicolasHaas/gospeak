package server

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestTemporaryChannelCreationRequiresParentMembership(t *testing.T) {
	srv, st, handler := newTestServer(t)
	parent := model.NewChannel()
	parent.Name = "parent"
	parent.AllowSubChannels = true
	if err := st.NonTx().CreateChannel(parent); err != nil {
		t.Fatalf("CreateChannel(parent): %v", err)
	}

	session := mustCreateSession(t, srv.sessions, 1, "creator", model.RoleUser)
	conn := &bufferConn{}
	srv.handleCreateChannel(session.ID, &pb.CreateChannelRequest{
		Name:     "unscoped-temp",
		ParentID: parent.ID,
		IsTemp:   true,
	}, st, conn, handler)

	response, err := protocol.ReadControlMessage(conn)
	if err != nil {
		t.Fatalf("ReadControlMessage(): %v", err)
	}
	if response.ErrorResponse == nil || response.ErrorResponse.Message != "join the parent channel before creating a sub-channel" {
		t.Fatalf("response = %#v, want parent-membership rejection", response)
	}
	channels, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatalf("ListChannels(): %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("temporary channel created without parent membership: channels = %#v", channels)
	}
}

func TestTemporaryChannelCreationEnforcesSessionScope(t *testing.T) {
	srv, st, handler := newTestServer(t)
	parent := model.NewChannel()
	parent.Name = "outside-scope"
	parent.AllowSubChannels = true
	if err := st.NonTx().CreateChannel(parent); err != nil {
		t.Fatalf("CreateChannel(parent): %v", err)
	}

	session := mustCreateScopedSession(t, srv.sessions, 1, "scoped", model.RoleUser, parent.ID+1)
	srv.channels.Join(session.ID, parent.ID)
	conn := &bufferConn{}
	srv.handleCreateChannel(session.ID, &pb.CreateChannelRequest{Name: "temp", ParentID: parent.ID, IsTemp: true}, st, conn, handler)

	response, err := protocol.ReadControlMessage(conn)
	if err != nil {
		t.Fatalf("ReadControlMessage(): %v", err)
	}
	if response.ErrorResponse == nil || response.ErrorResponse.Message != "parent channel is outside your invite scope" {
		t.Fatalf("response = %#v, want scope rejection", response)
	}
}

func TestTemporaryChannelUserQuotasPersistCreatorAndPruneCooldowns(t *testing.T) {
	for _, test := range []struct {
		name       string
		openServer bool
		limit      int
	}{
		{name: "invite only", limit: maxTempChannelsPerUser},
		{name: "open", openServer: true, limit: maxOpenTempChannelsPerUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, st, handler := newTestServerWithConfig(t, func(cfg *Config) {
				cfg.AllowNoToken = test.openServer
			})
			parent := model.NewChannel()
			parent.Name = "parent"
			parent.AllowSubChannels = true
			if err := st.NonTx().CreateChannel(parent); err != nil {
				t.Fatalf("CreateChannel(parent): %v", err)
			}
			session := mustCreateSession(t, srv.sessions, 7, "creator", model.RoleUser)
			srv.channels.Join(session.ID, parent.ID)

			now := time.Now()
			srv.tempChannels.now = func() time.Time { return now }
			for i := 0; i < test.limit; i++ {
				srv.handleCreateChannel(session.ID, &pb.CreateChannelRequest{
					Name: fmt.Sprintf("temp-%d", i), ParentID: parent.ID, IsTemp: true,
				}, st, &nopConn{}, handler)
				now = now.Add(tempChannelCreationCooldown)
			}

			conn := &bufferConn{}
			srv.handleCreateChannel(session.ID, &pb.CreateChannelRequest{Name: "over-quota", ParentID: parent.ID, IsTemp: true}, st, conn, handler)
			response, err := protocol.ReadControlMessage(conn)
			if err != nil {
				t.Fatalf("ReadControlMessage(): %v", err)
			}
			if response.ErrorResponse == nil || response.ErrorResponse.Message != "temporary channel capacity reached" {
				t.Fatalf("response = %#v, want quota rejection", response)
			}

			channels, err := st.NonTx().ListChannels()
			if err != nil {
				t.Fatalf("ListChannels(): %v", err)
			}
			creatorCount := 0
			for i := range channels {
				if channels[i].IsTemp && channels[i].CreatedBy == session.UserID {
					creatorCount++
				}
			}
			if creatorCount != test.limit {
				t.Fatalf("creator temporary channels = %d, want %d", creatorCount, test.limit)
			}
			if got := len(srv.tempChannels.lastCreation); got != 0 {
				t.Fatalf("expired cooldown entries = %d, want 0", got)
			}
		})
	}
}

func TestTemporaryChannelParentAndGlobalQuotas(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		parent := model.NewChannel()
		parent.Name = "parent"
		parent.AllowSubChannels = true
		if err := st.NonTx().CreateChannel(parent); err != nil {
			t.Fatalf("CreateChannel(parent): %v", err)
		}
		for i := 0; i < maxTempChannelsPerParent; i++ {
			channel := model.NewChannel()
			channel.Name = fmt.Sprintf("temp-%d", i)
			channel.ParentID = parent.ID
			channel.IsTemp = true
			channel.CreatedBy = int64(i + 1)
			if err := st.NonTx().CreateChannel(channel); err != nil {
				t.Fatalf("CreateChannel(temp %d): %v", i, err)
			}
		}
		session := mustCreateSession(t, srv.sessions, 100, "creator", model.RoleUser)
		srv.channels.Join(session.ID, parent.ID)
		snapshot, _ := srv.sessions.GetSnapshot(session.ID)
		_, err := srv.tempChannels.create(snapshot, &pb.CreateChannelRequest{ParentID: parent.ID, IsTemp: true}, srv.channels, st, "over", "")
		if !errors.Is(err, errTempChannelParentQuota) {
			t.Fatalf("create() error = %v, want %v", err, errTempChannelParentQuota)
		}
	})

	t.Run("global", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		parent := model.NewChannel()
		parent.Name = "parent"
		parent.AllowSubChannels = true
		if err := st.NonTx().CreateChannel(parent); err != nil {
			t.Fatalf("CreateChannel(parent): %v", err)
		}
		for i := 0; i < maxTempChannels; i++ {
			channel := model.NewChannel()
			channel.Name = fmt.Sprintf("temp-%d", i)
			channel.ParentID = int64(i/maxTempChannelsPerParent + 100)
			channel.IsTemp = true
			channel.CreatedBy = int64(i + 1)
			if err := st.NonTx().CreateChannel(channel); err != nil {
				t.Fatalf("CreateChannel(temp %d): %v", i, err)
			}
		}
		session := mustCreateSession(t, srv.sessions, 1000, "creator", model.RoleUser)
		srv.channels.Join(session.ID, parent.ID)
		snapshot, _ := srv.sessions.GetSnapshot(session.ID)
		_, err := srv.tempChannels.create(snapshot, &pb.CreateChannelRequest{ParentID: parent.ID, IsTemp: true}, srv.channels, st, "over", "")
		if !errors.Is(err, errTempChannelGlobalQuota) {
			t.Fatalf("create() error = %v, want %v", err, errTempChannelGlobalQuota)
		}
	})
}

func TestTemporaryChannelSweepDeletesEmptyAndPreservesOccupiedChannels(t *testing.T) {
	t.Run("empty after restart", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.DBPath = filepath.Join(t.TempDir(), "gospeak.db")
		firstStore, err := datastore.NewProviderFactory(cfg.DBPath)
		if err != nil {
			t.Fatalf("NewProviderFactory(first): %v", err)
		}
		parent := model.NewChannel()
		parent.Name = "parent"
		if err := firstStore.NonTx().CreateChannel(parent); err != nil {
			t.Fatalf("CreateChannel(parent): %v", err)
		}
		channel := model.NewChannel()
		channel.Name = "empty"
		channel.ParentID = parent.ID
		channel.IsTemp = true
		channel.CreatedBy = 1
		if err := firstStore.NonTx().CreateChannel(channel); err != nil {
			t.Fatalf("CreateChannel(temp): %v", err)
		}
		if err := firstStore.Close(); err != nil {
			t.Fatalf("Close(first): %v", err)
		}

		st, err := datastore.NewProviderFactory(cfg.DBPath)
		if err != nil {
			t.Fatalf("NewProviderFactory(restart): %v", err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("Close(restart): %v", err)
			}
		})
		srv := New(cfg, Dependencies{Store: st})
		handler := newControlHandler(srv, st)
		now := channel.CreatedAt.Add(time.Minute)
		srv.tempChannels.now = func() time.Time { return now }
		srv.sweepTempChannels(handler, st)
		now = now.Add(tempChannelEmptyGrace)
		srv.sweepTempChannels(handler, st)
		got, err := st.NonTx().GetChannel(channel.ID)
		if err != nil {
			t.Fatalf("GetChannel(): %v", err)
		}
		if got != nil {
			t.Fatalf("empty temporary channel survived grace period: %#v", got)
		}
	})

	t.Run("occupied channel has no maximum lifetime", func(t *testing.T) {
		srv, st, handler := newTestServer(t)
		parent := model.NewChannel()
		parent.Name = "parent"
		if err := st.NonTx().CreateChannel(parent); err != nil {
			t.Fatalf("CreateChannel(parent): %v", err)
		}
		channel := model.NewChannel()
		channel.Name = "occupied"
		channel.ParentID = parent.ID
		channel.IsTemp = true
		channel.CreatedBy = 1
		if err := st.NonTx().CreateChannel(channel); err != nil {
			t.Fatalf("CreateChannel(temp): %v", err)
		}
		session := mustCreateSession(t, srv.sessions, 1, "member", model.RoleUser)
		srv.channels.Join(session.ID, channel.ID)
		srv.sessions.SetChannel(session.ID, channel.ID)
		srv.tempChannels.now = func() time.Time { return channel.CreatedAt.Add(24 * time.Hour) }

		srv.sweepTempChannels(handler, st)
		if got, err := st.NonTx().GetChannel(channel.ID); err != nil || got == nil {
			t.Fatalf("occupied temporary channel = %#v, %v; want preserved", got, err)
		}
		if got := srv.channels.ChannelOf(session.ID); got != channel.ID {
			t.Fatalf("occupied member moved to channel %d, want %d", got, channel.ID)
		}
	})
}

func TestTemporaryChannelSweepPreservesReoccupiedChannelAndDeletesOrphan(t *testing.T) {
	srv, st, handler := newTestServer(t)
	parent := model.NewChannel()
	parent.Name = "parent"
	if err := st.NonTx().CreateChannel(parent); err != nil {
		t.Fatalf("CreateChannel(parent): %v", err)
	}
	occupied := model.NewChannel()
	occupied.Name = "occupied"
	occupied.ParentID = parent.ID
	occupied.IsTemp = true
	if err := st.NonTx().CreateChannel(occupied); err != nil {
		t.Fatalf("CreateChannel(occupied): %v", err)
	}
	orphan := model.NewChannel()
	orphan.Name = "orphan"
	orphan.ParentID = parent.ID + 999
	orphan.IsTemp = true
	if err := st.NonTx().CreateChannel(orphan); err != nil {
		t.Fatalf("CreateChannel(orphan): %v", err)
	}

	srv.tempChannels.emptySince[occupied.ID] = occupied.CreatedAt
	session := mustCreateSession(t, srv.sessions, 1, "member", model.RoleUser)
	if _, joined, err := srv.tempChannels.tryJoin(occupied, session.ID, srv.channels, srv.sessions, st); err != nil || !joined {
		t.Fatalf("tryJoin() joined = %t, err = %v", joined, err)
	}
	srv.tempChannels.now = func() time.Time { return occupied.CreatedAt.Add(tempChannelEmptyGrace) }
	srv.sweepTempChannels(handler, st)

	if got, err := st.NonTx().GetChannel(occupied.ID); err != nil || got == nil {
		t.Fatalf("reoccupied channel = %#v, err = %v; want preserved", got, err)
	}
	if got, err := st.NonTx().GetChannel(orphan.ID); err != nil || got != nil {
		t.Fatalf("orphan channel = %#v, err = %v; want deleted", got, err)
	}
}

func TestTemporaryChannelParentQuotaIsAtomic(t *testing.T) {
	srv, st, _ := newTestServer(t)
	parent := model.NewChannel()
	parent.Name = "parent"
	parent.AllowSubChannels = true
	if err := st.NonTx().CreateChannel(parent); err != nil {
		t.Fatalf("CreateChannel(parent): %v", err)
	}

	const attempts = maxTempChannelsPerParent * 2
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		session := mustCreateSession(t, srv.sessions, int64(i+1), fmt.Sprintf("creator-%d", i), model.RoleUser)
		srv.channels.Join(session.ID, parent.ID)
		snapshot, _ := srv.sessions.GetSnapshot(session.ID)
		wg.Add(1)
		go func(index int, current SessionSnapshot) {
			defer wg.Done()
			<-start
			_, err := srv.tempChannels.create(current, &pb.CreateChannelRequest{ParentID: parent.ID, IsTemp: true}, srv.channels, st, fmt.Sprintf("temp-%d", index), "")
			errs <- err
		}(i, snapshot)
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errTempChannelParentQuota):
		default:
			t.Fatalf("create() error = %v, want parent quota", err)
		}
	}
	if succeeded != maxTempChannelsPerParent {
		t.Fatalf("successful concurrent creations = %d, want %d", succeeded, maxTempChannelsPerParent)
	}
}

func TestTemporaryChannelEvictionDoesNotUndoLaterJoin(t *testing.T) {
	srv, st, _ := newTestServer(t)
	oldChannel := model.NewChannel()
	oldChannel.Name = "old"
	if err := st.NonTx().CreateChannel(oldChannel); err != nil {
		t.Fatalf("CreateChannel(old): %v", err)
	}
	newChannel := model.NewChannel()
	newChannel.Name = "new"
	if err := st.NonTx().CreateChannel(newChannel); err != nil {
		t.Fatalf("CreateChannel(new): %v", err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "member", model.RoleUser)
	srv.channels.Join(session.ID, oldChannel.ID)
	srv.sessions.SetChannel(session.ID, oldChannel.ID)

	srv.channels.Join(session.ID, newChannel.ID)
	srv.sessions.SetChannel(session.ID, newChannel.ID)
	if _, err := srv.screenShare.Start(newChannel.ID, session.ID, session.UserID, session.Username, 640, 480); err != nil {
		t.Fatalf("Start(new channel): %v", err)
	}
	if srv.evictChannelMemberIfCurrent(oldChannel.ID, session.ID, nil) {
		t.Fatal("stale eviction removed a later join")
	}
	if got := srv.channels.ChannelOf(session.ID); got != newChannel.ID {
		t.Fatalf("channel manager channel = %d, want %d", got, newChannel.ID)
	}
	snapshot, ok := srv.sessions.GetSnapshot(session.ID)
	if !ok || snapshot.ChannelID != newChannel.ID {
		t.Fatalf("session channel = %#v, want %d", snapshot, newChannel.ID)
	}
	if active, ok := srv.screenShare.ActiveForChannel(newChannel.ID); !ok || active.SessionID != session.ID {
		t.Fatalf("new-channel screen share removed by stale eviction: %#v, %t", active, ok)
	}
}

func TestTemporaryChannelEvictionCleansCurrentScreenState(t *testing.T) {
	srv, _, _ := newTestServer(t)
	const channelID int64 = 42
	session := mustCreateSession(t, srv.sessions, 1, "member", model.RoleUser)
	srv.channels.Join(session.ID, channelID)
	srv.sessions.SetChannel(session.ID, channelID)
	if _, err := srv.screenShare.Start(channelID, session.ID, session.UserID, session.Username, 640, 480); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if !srv.evictChannelMemberIfCurrent(channelID, session.ID, nil) {
		t.Fatal("current member was not evicted")
	}
	if _, ok := srv.screenShare.ActiveForChannel(channelID); ok {
		t.Fatal("screen share survived channel eviction")
	}
	if got := srv.channels.ChannelOf(session.ID); got != 0 {
		t.Fatalf("channel manager channel = %d, want 0", got)
	}
	snapshot, ok := srv.sessions.GetSnapshot(session.ID)
	if !ok || snapshot.ChannelID != 0 {
		t.Fatalf("session channel = %#v, want 0", snapshot)
	}
}

func TestTemporaryChannelCleanupDoesNotTrackPermanentChannel(t *testing.T) {
	srv, st, _ := newTestServer(t)
	channel := model.NewChannel()
	channel.Name = "permanent"
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatalf("CreateChannel(): %v", err)
	}
	srv.cleanupTempChannel(channel.ID, st)
	if _, tracked := srv.tempChannels.emptySince[channel.ID]; tracked {
		t.Fatal("permanent channel retained temporary lifecycle state")
	}
}
