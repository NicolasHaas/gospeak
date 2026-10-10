package server

import (
	"errors"
	"testing"
	"time"
)

func TestScreenShareStartReplacesSessionOwnershipAcrossChannels(t *testing.T) {
	m := NewScreenShareManager()
	old, err := m.Start(1, 10, 1, "sharer", 640, 480)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ShareWithViewers(10, []uint32{20}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Subscribe(1, 20); err != nil {
		t.Fatal(err)
	}
	m.lastFrameSequence[10] = 7
	m.lastFrameIngressAt[10] = time.Now()
	// A deletion can finish before an already snapshotted Start publishes. A
	// subsequent Start must not overwrite the sole owner index and orphan it.
	next, err := m.Start(2, 10, 1, "sharer", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ActiveForChannel(1); ok {
		t.Fatal("previous channel still owns the session's share")
	}
	if !m.IsSharer(10, 2) || string(old.EncryptionKey) == string(next.EncryptionKey) {
		t.Fatal("replacement share not installed with a fresh key")
	}
	if m.SubscriberCount() != 0 || len(m.authorizedTarget) != 0 || len(m.authorizedByShare[10]) != 0 || m.lastFrameSequence[10] != 0 || !m.lastFrameIngressAt[10].IsZero() {
		t.Fatal("replacement inherited previous share authorization or frame state")
	}
	if _, err := m.Subscribe(2, 20); err == nil {
		t.Fatal("old viewer retained authorization")
	}
	if _, ok := m.StopBySession(10); !ok {
		t.Fatal("replacement is not reachable for cleanup")
	}
	m.ExpireInactive(time.Nanosecond)
	if len(m.activeByChannel) != 0 || len(m.channelBySession) != 0 || len(m.cipherByShare) != 0 {
		t.Fatal("share ownership survived final cleanup")
	}
}

func TestScreenShareFailedReplacementPreservesCurrentShare(t *testing.T) {
	m := NewScreenShareManager()
	if _, err := m.Start(1, 10, 1, "first", 640, 480); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(2, 20, 2, "second", 640, 480); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(2, 10, 1, "first", 640, 480); !errors.Is(err, errScreenShareBusy) {
		t.Fatalf("busy replacement err=%v", err)
	}
	if _, err := m.Start(3, 10, 1, "first", 0, 480); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
	m.mediaCipher = "unsupported-fixture"
	if _, err := m.Start(3, 10, 1, "first", 640, 480); err == nil {
		t.Fatal("unsupported cipher accepted")
	}
	if !m.IsSharer(10, 1) || !m.IsSharer(20, 2) {
		t.Fatal("failed replacement removed an existing share")
	}
}
