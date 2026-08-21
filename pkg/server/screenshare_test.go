package server

import (
	"bytes"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type blockingScreenConn struct {
	nopConn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingScreenConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return len(p), nil
}

type signalingScreenConn struct {
	nopConn
	wrote chan struct{}
	once  sync.Once
}

func (c *signalingScreenConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.wrote) })
	return len(p), nil
}

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
	shared, viewerSessionIDs, err := mgr.ShareWithViewers(100, []uint32{300})
	if err != nil {
		t.Fatalf("ShareWithViewers: %v", err)
	}
	if len(viewerSessionIDs) != 1 || viewerSessionIDs[0] != 300 {
		t.Fatalf("ShareWithViewers viewers = %v, want [300]", viewerSessionIDs)
	}
	if len(shared.EncryptionKey) == 0 {
		t.Fatalf("ShareWithViewers returned empty encryption key")
	}

	subscribed, err := mgr.Subscribe(1, 300)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if subscribed.Viewers != 1 {
		t.Fatalf("Viewers = %d, want 1", subscribed.Viewers)
	}
	if len(subscribed.EncryptionKey) != 0 {
		t.Fatalf("Subscribe encryption key = %v, want empty", subscribed.EncryptionKey)
	}
	if string(shared.EncryptionKey) != string(started.EncryptionKey) {
		t.Fatalf("ShareWithViewers encryption key = %v, want %v", shared.EncryptionKey, started.EncryptionKey)
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
	if _, _, err := mgr.ShareWithViewers(10, []uint32{30}); err != nil {
		t.Fatalf("ShareWithViewers: %v", err)
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

func TestScreenShareManager_RestartBySameSessionClearsViewerState(t *testing.T) {
	mgr := NewScreenShareManager()

	first, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start(first): %v", err)
	}
	if _, viewers, err := mgr.ShareWithViewers(10, []uint32{30}); err != nil || len(viewers) != 1 {
		t.Fatalf("ShareWithViewers(first) = (%v, %v), want one viewer and nil error", viewers, err)
	}
	if _, err := mgr.Subscribe(1, 30); err != nil {
		t.Fatalf("Subscribe(first): %v", err)
	}

	replacement, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start(replacement): %v", err)
	}
	if bytes.Equal(first.EncryptionKey, replacement.EncryptionKey) {
		t.Fatal("replacement share reused encryption key")
	}
	if got := mgr.SubscribersForSharer(10); len(got) != 0 {
		t.Fatalf("replacement subscribers = %v, want none", got)
	}
	if got := mgr.SubscriberCount(); got != 0 {
		t.Fatalf("replacement subscriber count = %d, want 0", got)
	}
	if _, err := mgr.Subscribe(1, 30); err == nil {
		t.Fatal("viewer remained authorized across replacement share")
	}

	shared, viewers, err := mgr.ShareWithViewers(10, []uint32{30})
	if err != nil {
		t.Fatalf("ShareWithViewers(replacement): %v", err)
	}
	if len(viewers) != 1 || viewers[0] != 30 {
		t.Fatalf("replacement shared viewers = %v, want [30]", viewers)
	}
	if !bytes.Equal(shared.EncryptionKey, replacement.EncryptionKey) {
		t.Fatal("replacement viewer received the wrong encryption key")
	}
}

func TestScreenShareManager_ShareWithViewersRequiresActiveShare(t *testing.T) {
	mgr := NewScreenShareManager()

	if _, _, err := mgr.ShareWithViewers(100, []uint32{300}); err == nil {
		t.Fatalf("ShareWithViewers without active share = nil error, want error")
	}
}

func TestScreenShareManager_ExpireInactiveStopsShare(t *testing.T) {
	mgr := NewScreenShareManager()

	if _, err := mgr.Start(1, 10, 20, "alice", 800, 600); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mgr.lastFrameAt[10] = time.Now().Add(-20 * time.Second)

	expired := mgr.ExpireInactive(15 * time.Second)
	if len(expired) != 1 {
		t.Fatalf("ExpireInactive() len = %d, want 1", len(expired))
	}
	if expired[0].Active {
		t.Fatalf("expired event Active = true, want false")
	}
	if _, ok := mgr.ActiveForChannel(1); ok {
		t.Fatalf("ActiveForChannel(1) = ok true, want false")
	}
}

func TestScreenShareManagerAcceptFrameAuthenticatesBeforeMonotonicSequence(t *testing.T) {
	mgr := NewScreenShareManager()
	started, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cipher, err := gospeakCrypto.NewVoiceCipher(started.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	packet := func(sequence uint32, activeCipher *gospeakCrypto.VoiceCipher) *protocol.ScreenPacket {
		pkt := &protocol.ScreenPacket{SessionID: 10, SeqNum: sequence}
		pkt.Payload = activeCipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("frame"))
		return pkt
	}

	if !mgr.AcceptFrame(10, packet(1, cipher), 0) {
		t.Fatal("first authenticated frame was rejected")
	}
	if mgr.AcceptFrame(10, packet(1, cipher), 0) {
		t.Fatal("duplicate frame was accepted")
	}

	forged := packet(1000, cipher)
	forged.Payload[0] ^= 0xff
	if mgr.AcceptFrame(10, forged, 0) {
		t.Fatal("forged frame was accepted")
	}
	if !mgr.AcceptFrame(10, packet(2, cipher), 0) {
		t.Fatal("forged high sequence poisoned screen replay state")
	}
	if mgr.AcceptFrame(10, packet(1, cipher), 0) {
		t.Fatal("out-of-order screen frame was accepted on ordered transport")
	}

	if _, ok := mgr.StopBySession(10); !ok {
		t.Fatal("StopBySession failed")
	}
	restarted, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("restart share: %v", err)
	}
	if bytes.Equal(started.EncryptionKey, restarted.EncryptionKey) {
		t.Fatal("restart reused screen encryption key")
	}
	newCipher, err := gospeakCrypto.NewVoiceCipher(restarted.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher(restarted): %v", err)
	}
	if mgr.AcceptFrame(10, packet(3, cipher), 0) {
		t.Fatal("frame from previous share key was accepted")
	}
	if !mgr.AcceptFrame(10, packet(1, newCipher), 0) {
		t.Fatal("new share did not reset monotonic sequence state")
	}
}

func TestScreenShareManagerRateLimitedFrameStillConsumesSequence(t *testing.T) {
	mgr := NewScreenShareManager()
	started, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cipher, err := gospeakCrypto.NewVoiceCipher(started.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	pkt := &protocol.ScreenPacket{SessionID: 10, SeqNum: 1}
	pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("frame"))

	if mgr.AcceptFrame(10, pkt, time.Hour) {
		t.Fatal("frame inside rate-limit interval was allowed")
	}
	if mgr.AcceptFrame(10, pkt, 0) {
		t.Fatal("rate-limited frame sequence was reusable")
	}
}

func TestScreenShareManagerStaleAuthenticationCannotAdvanceReplacementShare(t *testing.T) {
	mgr := NewScreenShareManager()
	started, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldCipher, err := gospeakCrypto.NewVoiceCipher(started.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	oldPacket := &protocol.ScreenPacket{SessionID: 10, SeqNum: 1}
	oldPacket.Payload = oldCipher.Encrypt(oldPacket.SessionID, oldPacket.SeqNum, oldPacket.MarshalHeader(), []byte("old"))

	reached := make(chan struct{})
	release := make(chan struct{})
	mgr.beforeFrameReplayCommit = func() {
		close(reached)
		<-release
	}
	result := make(chan bool, 1)
	go func() { result <- mgr.AcceptFrame(10, oldPacket, 0) }()
	<-reached

	if _, ok := mgr.StopBySession(10); !ok {
		t.Fatal("StopBySession failed")
	}
	restarted, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	close(release)
	if <-result {
		t.Fatal("stale authentication advanced replacement share")
	}
	mgr.beforeFrameReplayCommit = nil

	newCipher, err := gospeakCrypto.NewVoiceCipher(restarted.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher(restarted): %v", err)
	}
	newPacket := &protocol.ScreenPacket{SessionID: 10, SeqNum: 1}
	newPacket.Payload = newCipher.Encrypt(newPacket.SessionID, newPacket.SeqNum, newPacket.MarshalHeader(), []byte("new"))
	if !mgr.AcceptFrame(10, newPacket, 0) {
		t.Fatal("replacement share sequence was poisoned")
	}
}

func TestScreenShareManagerAcceptFrameConcurrentDuplicateOnce(t *testing.T) {
	mgr := NewScreenShareManager()
	started, err := mgr.Start(1, 10, 20, "alice", 800, 600)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cipher, err := gospeakCrypto.NewVoiceCipher(started.EncryptionKey)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	pkt := &protocol.ScreenPacket{SessionID: 10, SeqNum: 1}
	pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("frame"))

	var accepted atomic.Int32
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if mgr.AcceptFrame(10, pkt, 0) {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("concurrent frame accepts = %d, want exactly 1", got)
	}
}

func TestSessionManager_ValidateScreenAuth(t *testing.T) {
	sm := NewSessionManager()
	session := mustCreateSession(t, sm, 1, "alice", 0)

	if !sm.ValidateScreenAuth(session.ID, session.ScreenAuthToken) {
		t.Fatalf("ValidateScreenAuth(valid) = false, want true")
	}
	if sm.ValidateScreenAuth(session.ID, "wrong") {
		t.Fatalf("ValidateScreenAuth(wrong) = true, want false")
	}
}

func TestScreenShareManager_ReassigningViewerRemovesOldAuthorization(t *testing.T) {
	mgr := NewScreenShareManager()
	if _, err := mgr.Start(1, 10, 20, "alice", 800, 600); err != nil {
		t.Fatalf("Start share A: %v", err)
	}
	if _, err := mgr.Start(2, 11, 21, "bob", 800, 600); err != nil {
		t.Fatalf("Start share B: %v", err)
	}
	if _, _, err := mgr.ShareWithViewers(10, []uint32{30}); err != nil {
		t.Fatalf("share A: %v", err)
	}
	if _, err := mgr.Subscribe(1, 30); err != nil {
		t.Fatalf("Subscribe(A): %v", err)
	}
	if _, _, err := mgr.ShareWithViewers(11, []uint32{30}); err != nil {
		t.Fatalf("share B: %v", err)
	}

	if mgr.authorizedByShare[10][30] {
		t.Fatalf("viewer remains authorized for old share")
	}
	if mgr.subscribersByShare[10][30] {
		t.Fatalf("viewer remains subscribed to old share")
	}
	if !mgr.authorizedByShare[11][30] || mgr.authorizedTarget[30] != 11 {
		t.Fatalf("viewer authorization does not point only to new share")
	}

	if _, ok := mgr.StopBySession(10); !ok {
		t.Fatalf("StopBySession(A): ok = false, want true")
	}
	if _, err := mgr.Subscribe(2, 30); err != nil {
		t.Fatalf("Subscribe(B) after stopping A: %v", err)
	}
}

func TestScreenRelay_SlowViewerDoesNotBlockOtherViewer(t *testing.T) {
	s := New(DefaultConfig(), Dependencies{})
	slow := &blockingScreenConn{started: make(chan struct{}), release: make(chan struct{})}
	fast := &signalingScreenConn{wrote: make(chan struct{})}
	s.setScreenConn(1, slow)
	s.setScreenConn(2, fast)
	t.Cleanup(func() {
		close(slow.release)
		s.closeScreenConns()
	})

	pkt := &protocol.ScreenPacket{SessionID: 9, SeqNum: 1, Payload: []byte("frame")}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sendScreenPacketToSession(1, pkt)
		s.sendScreenPacketToSession(2, pkt)
	}()

	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("slow viewer write did not start")
	}
	select {
	case <-fast.wrote:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("fast viewer was blocked by slow viewer")
	}
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("screen relay remained blocked on slow viewer")
	}
}

var _ net.Conn = (*blockingScreenConn)(nil)
var _ net.Conn = (*signalingScreenConn)(nil)
