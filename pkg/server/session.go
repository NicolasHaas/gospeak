package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// SessionManager manages active client sessions.
type SessionManager struct {
	mu               sync.RWMutex
	sessions         map[uint32]*model.Session // sessionID -> session
	voiceReplay      map[uint32]*voiceReceiveState
	maxSessions      int
	maxPerUser       int
	maxPerSource     int
	bySource         map[netip.Prefix]int // pending and active claims
	sessionSources   map[uint32]netip.Prefix
	pending          int
	pendingByUser    map[int64]int
	highWater        int
	userHighWater    int
	sourceHighWater  int
	nextSessionID    uint32
	issuedSessionIDs uint64
	sessionIDSeeded  bool
	voiceClosed      bool
}

type voiceReceiveState struct {
	protocol.ReplayWindow
	packets, bytes float64
	updated        time.Time
	challenge      [16]byte
	challengeAddr  *net.UDPAddr
	challengeUntil time.Time
}

const (
	voiceChallengeTTL = 3 * time.Second
	voiceEndpointTTL  = 30 * time.Second
)

var (
	ErrSessionIDExhausted        = errors.New("server: session ID space exhausted")
	ErrGlobalSessionLimitReached = errors.New("server: global session limit reached")
	ErrUserSessionLimitReached   = errors.New("server: per-user session limit reached")
	ErrSourceSessionLimitReached = errors.New("server: per-source session limit reached")
)

// SessionCapacityError reports the synchronized occupancy that rejected a session.
type SessionCapacityError struct {
	Cause   error
	Current int
	Limit   int
}

func (err *SessionCapacityError) Error() string { return err.Cause.Error() }
func (err *SessionCapacityError) Unwrap() error { return err.Cause }

// SessionCapacitySnapshot is a synchronized view of authenticated session use.
type SessionCapacitySnapshot struct {
	Active                  int
	CapacityUsed            int
	MaxUserCapacity         int
	GlobalLimit             int
	PerUserLimit            int
	CapacityHighWater       int
	UserCapacityHighWater   int
	MaxSourceCapacity       int
	PerSourceLimit          int
	SourceCapacityHighWater int
	ActiveSources           int
}

// SessionReservation owns pending capacity until it creates a session or is released.
type SessionReservation struct {
	manager  *SessionManager
	userID   int64
	source   netip.Prefix
	active   bool
	prepared *model.Session
}

const usableSessionIDs = uint64(math.MaxUint32) - 1 // zero and the registration magic are reserved

// SessionSnapshot is an immutable view of a session.
type SessionSnapshot struct {
	ID              uint32
	UserID          int64
	Username        string
	Role            model.Role
	ChannelScope    int64
	ChannelID       int64
	ScreenAuthToken string
	UDPAddr         *net.UDPAddr
	Muted           bool
	Deafened        bool
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return NewSessionManagerWithLimits(0, 0, 0)
}

// NewSessionManagerWithLimits creates a session manager with optional global,
// per-user and per-source limits. Non-positive limits are disabled.
func NewSessionManagerWithLimits(maxSessions, maxPerUser, maxPerSource int) *SessionManager {
	return &SessionManager{
		sessions:       make(map[uint32]*model.Session),
		voiceReplay:    make(map[uint32]*voiceReceiveState),
		pendingByUser:  make(map[int64]int),
		maxSessions:    maxSessions,
		maxPerUser:     maxPerUser,
		maxPerSource:   maxPerSource,
		bySource:       make(map[netip.Prefix]int),
		sessionSources: make(map[uint32]netip.Prefix),
	}
}

// Create creates a new session for an authenticated user.
func (sm *SessionManager) Create(userID int64, username string, role model.Role, sourceIP string) (*model.Session, error) {
	return sm.CreateWithChannelScope(userID, username, role, 0, sourceIP)
}

// CreateWithChannelScope creates a session with a persistent invite restriction.
func (sm *SessionManager) CreateWithChannelScope(userID int64, username string, role model.Role, channelScope int64, sourceIP string) (*model.Session, error) {
	reservation, err := sm.Reserve(userID, sourceIP)
	if err != nil {
		return nil, err
	}
	defer reservation.Release()
	return reservation.Create(username, role, channelScope)
}

// Reserve atomically claims global, account and source capacity. A zero user ID
// must be bound before Prepare. Source IPs are exact addresses, not CIDRs.
func (sm *SessionManager) Reserve(userID int64, sourceIP string) (*SessionReservation, error) {
	source, err := sessionSourceGroup(sourceIP)
	if err != nil {
		return nil, err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	globalUsage := len(sm.sessions) + sm.pending
	if sm.maxSessions > 0 && globalUsage >= sm.maxSessions {
		return nil, &SessionCapacityError{Cause: ErrGlobalSessionLimitReached, Current: globalUsage, Limit: sm.maxSessions}
	}
	if err := sm.checkUserCapacityLocked(userID); err != nil {
		return nil, err
	}
	if usage := sm.bySource[source]; sm.maxPerSource > 0 && usage >= sm.maxPerSource {
		return nil, &SessionCapacityError{Cause: ErrSourceSessionLimitReached, Current: usage, Limit: sm.maxPerSource}
	}
	sm.bySource[source]++
	sm.sourceHighWater = max(sm.sourceHighWater, sm.bySource[source])
	sm.pending++
	if userID != 0 {
		sm.pendingByUser[userID]++
	}
	if usage := len(sm.sessions) + sm.pending; usage > sm.highWater {
		sm.highWater = usage
	}
	if usage := sm.userUsageLocked(userID); usage > sm.userHighWater {
		sm.userHighWater = usage
	}
	return &SessionReservation{manager: sm, userID: userID, source: source, active: true}, nil
}

func sessionSourceGroup(sourceIP string) (netip.Prefix, error) {
	addr, err := netip.ParseAddr(sourceIP)
	if err != nil || addr.Zone() != "" {
		return netip.Prefix{}, errors.New("server: invalid session source IP")
	}
	addr = addr.Unmap()
	bits := 64
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits).Masked(), nil
}

func (sm *SessionManager) checkUserCapacityLocked(userID int64) error {
	if userID == 0 || sm.maxPerUser <= 0 {
		return nil
	}
	usage := sm.userUsageLocked(userID)
	if usage >= sm.maxPerUser {
		return &SessionCapacityError{Cause: ErrUserSessionLimitReached, Current: usage, Limit: sm.maxPerUser}
	}
	return nil
}

func (sm *SessionManager) userUsageLocked(userID int64) int {
	if userID == 0 {
		return 0
	}
	usage := sm.pendingByUser[userID]
	for _, session := range sm.sessions {
		if session.UserID == userID {
			usage++
		}
	}
	return usage
}

// BindUser adds the per-user claim to a global/source reservation.
func (reservation *SessionReservation) BindUser(userID int64) error {
	if reservation == nil || reservation.manager == nil || userID == 0 {
		return errors.New("server: invalid session reservation")
	}
	sm := reservation.manager
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !reservation.active {
		return errors.New("server: inactive session reservation")
	}
	if reservation.userID == userID {
		return nil
	}
	if reservation.userID != 0 {
		return errors.New("server: session reservation already bound")
	}
	if err := sm.checkUserCapacityLocked(userID); err != nil {
		return err
	}
	reservation.userID = userID
	sm.pendingByUser[userID]++
	if usage := sm.userUsageLocked(userID); usage > sm.userHighWater {
		sm.userHighWater = usage
	}
	return nil
}

// Prepare allocates every fallible session resource while retaining reserved capacity.
func (reservation *SessionReservation) Prepare(username string, role model.Role, channelScope int64) (*model.Session, error) {
	if reservation == nil || reservation.manager == nil {
		return nil, errors.New("server: invalid session reservation")
	}
	sm := reservation.manager
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !reservation.active || reservation.userID == 0 {
		return nil, errors.New("server: inactive or unbound session reservation")
	}
	if reservation.prepared != nil {
		return nil, errors.New("server: session reservation already prepared")
	}
	userID := reservation.userID

	id, err := sm.nextIDLocked()
	if err != nil {
		return nil, err
	}
	registrationKey := make([]byte, protocol.VoiceRegistrationKeySize)
	if _, err := rand.Read(registrationKey); err != nil {
		return nil, fmt.Errorf("server: generate voice registration key: %w", err)
	}
	screenAuthToken, err := newScreenAuthToken()
	if err != nil {
		return nil, err
	}

	reservation.prepared = &model.Session{
		ID:                   id,
		UserID:               userID,
		Username:             username,
		Role:                 role,
		ChannelScope:         channelScope,
		ScreenAuthToken:      screenAuthToken,
		VoiceRegistrationKey: registrationKey,
	}
	return reservation.prepared, nil
}

// Activate installs a prepared session without another fallible operation.
func (reservation *SessionReservation) Activate() *model.Session {
	if reservation == nil || reservation.manager == nil {
		return nil
	}
	sm := reservation.manager
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !reservation.active || reservation.prepared == nil {
		return nil
	}
	sess := reservation.prepared
	userID := reservation.userID
	sm.sessions[sess.ID] = sess
	sm.sessionSources[sess.ID] = reservation.source
	sm.bySource[reservation.source]++ // transfer the claim before releasing pending capacity
	sm.releaseReservationLocked(reservation)
	if len(sm.sessions) > sm.highWater {
		sm.highWater = len(sm.sessions)
	}
	userSessions := 0
	for _, active := range sm.sessions {
		if active.UserID == userID {
			userSessions++
		}
	}
	if userSessions > sm.userHighWater {
		sm.userHighWater = userSessions
	}
	return sess
}

// Create prepares and immediately activates a session.
func (reservation *SessionReservation) Create(username string, role model.Role, channelScope int64) (*model.Session, error) {
	if _, err := reservation.Prepare(username, role, channelScope); err != nil {
		return nil, err
	}
	session := reservation.Activate()
	if session == nil {
		return nil, errors.New("server: prepared session activation failed")
	}
	return session, nil
}

// Release returns unused reserved capacity. It is safe to call repeatedly.
func (reservation *SessionReservation) Release() {
	if reservation == nil || reservation.manager == nil {
		return
	}
	sm := reservation.manager
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.releaseReservationLocked(reservation)
}

func (sm *SessionManager) releaseReservationLocked(reservation *SessionReservation) {
	if !reservation.active {
		return
	}
	reservation.active = false
	sm.pending--
	sm.releaseSourceLocked(reservation.source)
	if reservation.userID != 0 {
		sm.pendingByUser[reservation.userID]--
		if sm.pendingByUser[reservation.userID] == 0 {
			delete(sm.pendingByUser, reservation.userID)
		}
	}
}

func (sm *SessionManager) releaseSourceLocked(source netip.Prefix) {
	sm.bySource[source]--
	if sm.bySource[source] == 0 {
		delete(sm.bySource, source)
	}
}

// nextIDLocked allocates each usable uint32 exactly once for this manager's
// lifetime. The server voice key is scoped to the same server lifecycle, so a
// reused (session ID, sequence number) AEAD nonce is impossible.
func (sm *SessionManager) nextIDLocked() (uint32, error) {
	if sm.issuedSessionIDs >= usableSessionIDs {
		return 0, ErrSessionIDExhausted
	}
	if !sm.sessionIDSeeded {
		var seed [4]byte
		if _, err := rand.Read(seed[:]); err != nil {
			return 0, fmt.Errorf("server: seed session IDs: %w", err)
		}
		sm.nextSessionID = binary.BigEndian.Uint32(seed[:])
		sm.sessionIDSeeded = true
	}
	for {
		id := sm.nextSessionID
		sm.nextSessionID++
		if id == 0 || id == protocol.VoiceRegistrationMagic {
			continue
		}
		sm.issuedSessionIDs++
		return id, nil
	}
}

// GetSnapshot returns an immutable snapshot of the session by session ID.
func (sm *SessionManager) GetSnapshot(sessionID uint32) (SessionSnapshot, bool) {
	return sm.getSnapshotAt(sessionID, time.Now())
}

func (sm *SessionManager) getSnapshotAt(sessionID uint32, now time.Time) (SessionSnapshot, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	if !ok {
		return SessionSnapshot{}, false
	}
	return SessionSnapshot{
		ID:              s.ID,
		UserID:          s.UserID,
		Username:        s.Username,
		Role:            s.Role,
		ChannelScope:    s.ChannelScope,
		ChannelID:       s.ChannelID,
		ScreenAuthToken: s.ScreenAuthToken,
		UDPAddr:         cloneUDPAddr(sm.voiceEndpointLocked(s, now)),
		Muted:           s.Muted,
		Deafened:        s.Deafened,
	}, true
}

// GetAllByUserIDSnapshots retrieves immutable snapshots for every session of a user.
func (sm *SessionManager) GetAllByUserIDSnapshots(userID int64) []SessionSnapshot {
	now := time.Now()
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	snapshots := make([]SessionSnapshot, 0)
	for _, s := range sm.sessions {
		if s.UserID == userID {
			snapshots = append(snapshots, SessionSnapshot{
				ID:              s.ID,
				UserID:          s.UserID,
				Username:        s.Username,
				Role:            s.Role,
				ChannelScope:    s.ChannelScope,
				ChannelID:       s.ChannelID,
				ScreenAuthToken: s.ScreenAuthToken,
				UDPAddr:         cloneUDPAddr(sm.voiceEndpointLocked(s, now)),
				Muted:           s.Muted,
				Deafened:        s.Deafened,
			})
		}
	}
	return snapshots
}

// ValidateScreenAuth reports whether the session exists and the auth token matches.
func (sm *SessionManager) ValidateScreenAuth(sessionID uint32, token string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.ScreenAuthToken), []byte(token)) == 1
}

// Remove removes a session and its replay state.
func (sm *SessionManager) Remove(id uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if source, ok := sm.sessionSources[id]; ok {
		sm.releaseSourceLocked(source)
		delete(sm.sessionSources, id)
	}
	delete(sm.sessions, id)
	delete(sm.voiceReplay, id)
}

// AcceptVoiceSequence atomically rechecks an authenticated packet's endpoint
// and records its sequence for an active, unmuted session. Authentication must
// happen before this call so forged high sequences cannot advance the window.
func (sm *SessionManager) AcceptVoiceSequence(id uint32, expectedAddr *net.UDPAddr, sequence uint32, now time.Time) (SessionSnapshot, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[id]
	if !ok || s.Muted || !udpAddrEqual(sm.voiceEndpointLocked(s, now), expectedAddr) {
		return SessionSnapshot{}, false
	}
	window := sm.voiceReplay[id]
	if window == nil {
		window = &voiceReceiveState{}
		sm.voiceReplay[id] = window
	}
	if !window.Accept(sequence) {
		return SessionSnapshot{}, false
	}
	return SessionSnapshot{
		ID:              s.ID,
		UserID:          s.UserID,
		Username:        s.Username,
		Role:            s.Role,
		ChannelScope:    s.ChannelScope,
		ChannelID:       s.ChannelID,
		ScreenAuthToken: s.ScreenAuthToken,
		UDPAddr:         cloneUDPAddr(sm.voiceEndpointLocked(s, now)),
		Muted:           s.Muted,
		Deafened:        s.Deafened,
	}, true
}

// allowVoicePacket charges only authenticated, replay-accepted packets.
func (sm *SessionManager) allowVoicePacket(id uint32, addr *net.UDPAddr, channelID int64, packetBytes int, now time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	session, state := sm.sessions[id], sm.voiceReplay[id]
	if session == nil || state == nil || session.Muted || channelID <= 0 || session.ChannelID != channelID || !udpAddrEqual(sm.voiceEndpointLocked(session, now), addr) || packetBytes <= 0 {
		return false
	}
	if !state.charge(packetBytes, now) {
		return false
	}
	session.VoiceEndpointLastSeen = now
	return true
}

// voiceEndpointLocked is the shared expiry gate for snapshots and receive commits.
// Expiry does not reset counters, replay state or the rebind clock.
func (sm *SessionManager) voiceEndpointLocked(s *model.Session, now time.Time) *net.UDPAddr {
	if sm.voiceClosed || s.VoiceEndpointLastSeen.IsZero() || !now.Before(s.VoiceEndpointLastSeen.Add(voiceEndpointTTL)) {
		return nil
	}
	return s.UDPAddr
}

func (sm *SessionManager) closeVoiceEndpoints() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.voiceClosed = true
}

// RegisterUDPAddr processes GSR2 under the session lifecycle lock. A true result
// means accepted work, not endpoint ownership: a non-nil reply must be returned
// to the observed address, which must answer before a destination is installed.
// One pending challenge per session bounds state; both legs share the voice budget.
func (sm *SessionManager) RegisterUDPAddr(id uint32, registration *protocol.VoiceRegistration, addr *net.UDPAddr, now time.Time, rebindInterval time.Duration) ([]byte, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[id]
	if sm.voiceClosed || !ok || addr == nil || addr.Port <= 0 || addr.Port > 65535 || addr.IP.To16() == nil || registration == nil || registration.SessionID != id || !registration.Verify(s.VoiceRegistrationKey) {
		return nil, false
	}
	rebound := !udpAddrEqual(s.UDPAddr, addr)
	if rebound && s.UDPAddr != nil && now.Sub(s.VoiceEndpointUpdatedAt) < rebindInterval {
		return nil, false
	}
	state := sm.voiceReplay[id]
	switch registration.Kind {
	case protocol.VoiceRegistrationResponse:
		if state == nil || registration.Counter != s.VoiceRegistrationCounter || !udpAddrEqual(state.challengeAddr, addr) || registration.Challenge != state.challenge || !now.Before(state.challengeUntil) || now.Before(state.challengeUntil.Add(-voiceChallengeTTL)) {
			return nil, false
		}
		if !sm.chargeRegistrationToken(id, now) {
			return nil, false
		}
		state.challengeAddr = nil
	case protocol.VoiceRegistrationRequest:
		if registration.Counter <= s.VoiceRegistrationCounter {
			return nil, false
		}
		if !sm.chargeRegistrationToken(id, now) {
			return nil, false
		}
		state = sm.voiceReplay[id]
		state.challengeAddr = nil
		if !udpAddrEqual(sm.voiceEndpointLocked(s, now), addr) {
			var challenge [16]byte
			if _, err := rand.Read(challenge[:]); err != nil {
				return nil, false
			}
			reply, err := (&protocol.VoiceRegistration{SessionID: id, Counter: registration.Counter, Kind: protocol.VoiceRegistrationChallenge, Challenge: challenge}).Marshal(s.VoiceRegistrationKey)
			if err != nil {
				return nil, false
			}
			state.challenge, state.challengeAddr, state.challengeUntil = challenge, cloneUDPAddr(addr), now.Add(voiceChallengeTTL)
			s.VoiceRegistrationCounter = registration.Counter
			return reply, true
		}
		s.VoiceRegistrationCounter = registration.Counter
	default:
		return nil, false
	}
	if rebound {
		s.VoiceEndpointUpdatedAt = now
	}
	s.UDPAddr = cloneUDPAddr(addr)
	s.VoiceEndpointLastSeen = now
	return nil, true
}

func (sm *SessionManager) chargeRegistrationToken(id uint32, now time.Time) bool {
	state := sm.voiceReplay[id]
	if state == nil {
		state = &voiceReceiveState{}
		sm.voiceReplay[id] = state
	}
	return state.charge(protocol.VoiceHeaderSize+protocol.MaxVoicePayload, now)
}

// charge consumes one packet token and its bytes from the shared voice-plane
// budget, refilling at the legal 50 Hz rate up to the burst bound.
func (state *voiceReceiveState) charge(packetBytes int, now time.Time) bool {
	const packetBudgetBytes = voicePacketBurst * (protocol.VoiceHeaderSize + protocol.MaxVoicePayload)
	const budgetBytesPerSecond = voicePacketsPerSecond * (protocol.VoiceHeaderSize + protocol.MaxVoicePayload)
	if state.updated.IsZero() {
		state.packets, state.bytes, state.updated = voicePacketBurst, packetBudgetBytes, now
	}
	if elapsed := now.Sub(state.updated).Seconds(); elapsed > 0 {
		state.packets = min(voicePacketBurst, state.packets+elapsed*voicePacketsPerSecond)
		state.bytes = min(float64(packetBudgetBytes), state.bytes+elapsed*budgetBytesPerSecond)
		state.updated = now
	}
	if state.packets < 1 || state.bytes < float64(packetBytes) {
		return false
	}
	state.packets--
	state.bytes -= float64(packetBytes)
	return true
}

// UpdateUserState reports whether muted/deafened changed for an existing session.
func (sm *SessionManager) UpdateUserState(id uint32, muted, deafened bool) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[id]
	if !ok || (s.Muted == muted && s.Deafened == deafened) {
		return false
	}
	s.Muted = muted
	s.Deafened = deafened
	return true
}

// SetChannel sets the channel ID for a session.
func (sm *SessionManager) SetChannel(id uint32, channelID int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		s.ChannelID = channelID
	}
}

// ClearChannelIf clears a session's channel only if it has not moved since
// cleanup selected it.
func (sm *SessionManager) ClearChannelIf(id uint32, expectedChannelID int64) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[id]
	if !ok || s.ChannelID != expectedChannelID {
		return false
	}
	s.ChannelID = 0
	return true
}

// UpdateRoleByUserID updates the role for every active session of a user.
func (sm *SessionManager) UpdateRoleByUserID(userID int64, role model.Role) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, s := range sm.sessions {
		if s.UserID == userID {
			s.Role = role
		}
	}
}

// Count returns the number of active sessions.
func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

// CapacitySnapshot reports current and startup-lifetime session pressure.
func (sm *SessionManager) CapacitySnapshot() SessionCapacitySnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	byUser := make(map[int64]int, len(sm.pendingByUser))
	for userID, pending := range sm.pendingByUser {
		byUser[userID] = pending
	}
	maxByUser := 0
	for _, pending := range byUser {
		if pending > maxByUser {
			maxByUser = pending
		}
	}
	for _, session := range sm.sessions {
		byUser[session.UserID]++
		if byUser[session.UserID] > maxByUser {
			maxByUser = byUser[session.UserID]
		}
	}
	maxBySource := 0
	for _, count := range sm.bySource {
		maxBySource = max(maxBySource, count)
	}
	return SessionCapacitySnapshot{
		MaxSourceCapacity:       maxBySource,
		PerSourceLimit:          sm.maxPerSource,
		SourceCapacityHighWater: sm.sourceHighWater,
		ActiveSources:           len(sm.bySource),
		Active:                  len(sm.sessions),
		CapacityUsed:            len(sm.sessions) + sm.pending,
		MaxUserCapacity:         maxByUser,
		GlobalLimit:             sm.maxSessions,
		PerUserLimit:            sm.maxPerUser,
		CapacityHighWater:       sm.highWater,
		UserCapacityHighWater:   sm.userHighWater,
	}
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	clone := *addr
	if addr.IP != nil {
		clone.IP = append([]byte(nil), addr.IP...)
	}
	return &clone
}

func udpAddrEqual(left, right *net.UDPAddr) bool {
	return left != nil && right != nil && left.IP.Equal(right.IP) && left.Port == right.Port && left.Zone == right.Zone
}

func newScreenAuthToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("server: generate screen auth token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
