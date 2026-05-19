package server

import "testing"

func TestScreenShareManager_PublicEventHidesEncryptionKey(t *testing.T) {
	mgr := NewScreenShareManager()

	started, err := mgr.Start(1, 11, 22, "alice", 1280, 720)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(started.EncryptionKey) == 0 {
		t.Fatalf("Start returned empty encryption key")
	}

	publicEvent, ok := mgr.PublicEvent(1)
	if !ok {
		t.Fatalf("PublicEvent: missing event")
	}
	if len(publicEvent.EncryptionKey) != 0 {
		t.Fatalf("PublicEvent encryption key = %v, want empty", publicEvent.EncryptionKey)
	}
}

func TestScreenShareManager_SubscribeTracksViewerAndReturnsKey(t *testing.T) {
	mgr := NewScreenShareManager()

	started, err := mgr.Start(1, 100, 200, "alice", 1920, 1080)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	subscribed, err := mgr.Subscribe(1, 300)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if subscribed.Viewers != 1 {
		t.Fatalf("Viewers = %d, want 1", subscribed.Viewers)
	}
	if len(subscribed.EncryptionKey) == 0 {
		t.Fatalf("Subscribe returned empty encryption key")
	}
	if string(subscribed.EncryptionKey) != string(started.EncryptionKey) {
		t.Fatalf("Subscribe encryption key = %v, want %v", subscribed.EncryptionKey, started.EncryptionKey)
	}

	viewers := mgr.SubscribersForSharer(100)
	if len(viewers) != 1 || viewers[0] != 300 {
		t.Fatalf("SubscribersForSharer = %v, want [300]", viewers)
	}
	if got := mgr.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}
}

func TestScreenShareManager_StopClearsSubscribers(t *testing.T) {
	mgr := NewScreenShareManager()

	if _, err := mgr.Start(1, 10, 20, "alice", 800, 600); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := mgr.Subscribe(1, 30); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	stopped, ok := mgr.StopBySession(10)
	if !ok {
		t.Fatalf("StopBySession: ok = false, want true")
	}
	if stopped.Active {
		t.Fatalf("stopped.Active = true, want false")
	}
	if got := mgr.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount = %d, want 0", got)
	}
	if _, ok := mgr.ActiveForChannel(1); ok {
		t.Fatalf("ActiveForChannel(1) = ok true, want false")
	}
}

func TestSessionManager_ValidateScreenAuth(t *testing.T) {
	sm := NewSessionManager()
	session := sm.Create(1, "alice", 0)

	if !sm.ValidateScreenAuth(session.ID, session.ScreenAuthToken) {
		t.Fatalf("ValidateScreenAuth(valid) = false, want true")
	}
	if sm.ValidateScreenAuth(session.ID, "wrong") {
		t.Fatalf("ValidateScreenAuth(wrong) = true, want false")
	}
}
