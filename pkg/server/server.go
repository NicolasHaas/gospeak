// Package server implements the GoSpeak server.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// Config holds server configuration.
type Config struct {
	ControlAddr           string        // TCP/TLS bind address (e.g. ":9600")
	VoiceAddr             string        // UDP bind address (e.g. ":9601")
	ScreenAddr            string        // TCP/TLS screen-share bind address (e.g. ":9603")
	DBPath                string        // SQLite database path
	CertFile              string        // TLS certificate file path
	KeyFile               string        // TLS private key file path
	DataDir               string        // directory for generated certs and data
	AllowNoToken          bool          // allow users to join without a token (open server)
	EnableScreenShare     bool          // enable per-channel screen sharing support
	ChannelsFile          string        // YAML file defining channels to create on startup
	MetricsAddr           string        // HTTP bind address for /metrics endpoint (empty = disabled)
	PreAuthTimeout        time.Duration // maximum TLS handshake and authentication time
	MaxPreAuthConnections int           // maximum concurrent unauthenticated connections per TCP plane

	// CLI-only actions (run and exit)
	ExportUsers    bool // export all users as YAML and exit
	ExportChannels bool // export all channels as YAML and exit
}

// Dependencies holds external dependencies for the server.
// The caller retains ownership of Store and must close it after Run returns.
type Dependencies struct {
	Store datastore.DataProviderFactory
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ControlAddr:           ":9600",
		VoiceAddr:             ":9601",
		ScreenAddr:            ":9603",
		MetricsAddr:           "",
		DBPath:                "gospeak.db",
		DataDir:               ".",
		PreAuthTimeout:        10 * time.Second,
		MaxPreAuthConnections: 64,
	}
}

// Server is the main GoSpeak server.
type Server struct {
	cfg                     Config
	sessions                *SessionManager
	channels                *ChannelManager
	screenShare             *ScreenShareManager
	metrics                 *Metrics
	store                   datastore.DataProviderFactory
	listenerMu              sync.Mutex
	controlConn             net.Listener
	voiceConn               *net.UDPConn
	screenConn              net.Listener
	controlStarting         bool
	voiceStarting           bool
	screenStarting          bool
	metricsMu               sync.Mutex
	metricsHTTP             *http.Server
	metricsConn             net.Listener
	shutdownOnce            sync.Once
	workerMu                sync.Mutex
	workers                 sync.WaitGroup
	stopping                bool
	screenMu                sync.RWMutex
	screenConns             map[uint32]*screenClientConn
	voiceKey                []byte // shared AES-128 key for all voice encryption
	voiceCipher             *gospeakCrypto.VoiceCipher
	voiceReplayHook         func()
	authLimiter             *authRateLimiter
	accountProvisionLimiter *accountProvisionLimiter
	bootstrapMu             sync.Mutex
	preAuthMu               sync.Mutex
	acceptedConns           map[net.Conn]trackedConn
	preAuthCount            map[preAuthPlane]int
	preAuthByIP             map[preAuthPlane]map[string]int

	// Per-session voice debug counters (reset each debug interval; only used when debug is enabled)
	voiceDebugEnabled bool
	voiceStats        map[uint32]*perSessionVoiceStat
	voiceStatsMu      sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new Server instance.
func New(cfg Config, deps Dependencies) *Server {
	if cfg.PreAuthTimeout <= 0 {
		cfg.PreAuthTimeout = 10 * time.Second
	}
	if cfg.MaxPreAuthConnections <= 0 {
		cfg.MaxPreAuthConnections = 64
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:                     cfg,
		sessions:                NewSessionManager(),
		channels:                NewChannelManager(),
		screenShare:             NewScreenShareManager(),
		metrics:                 NewMetrics(),
		screenConns:             make(map[uint32]*screenClientConn),
		voiceStats:              make(map[uint32]*perSessionVoiceStat),
		store:                   deps.Store,
		authLimiter:             newAuthRateLimiter(authRateLimitAttempts, authRateLimitWindow),
		accountProvisionLimiter: newAccountProvisionLimiter(accountProvisionLimit, accountProvisionWindow),
		acceptedConns:           make(map[net.Conn]trackedConn),
		preAuthCount:            make(map[preAuthPlane]int),
		preAuthByIP:             make(map[preAuthPlane]map[string]int),
		ctx:                     ctx,
		cancel:                  cancel,
	}
}

// beginTask registers server-owned work unless shutdown has begun.
func (s *Server) beginTask() bool {
	s.workerMu.Lock()
	if s.stopping {
		s.workerMu.Unlock()
		return false
	}
	s.workers.Add(1)
	s.workerMu.Unlock()
	return true
}

func (s *Server) endTask() {
	s.workers.Done()
}

// startWorker starts and tracks a server-owned goroutine.
func (s *Server) startWorker(worker func()) bool {
	if !s.beginTask() {
		return false
	}
	go func() {
		defer s.endTask()
		worker()
	}()
	return true
}

func (s *Server) trackHTTPHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.beginTask() {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		defer s.endTask()
		handler.ServeHTTP(w, r)
	})
}

type preAuthPlane string

type trackedConn struct {
	plane        preAuthPlane
	preAuth      bool
	admissionKey string
}

const (
	preAuthControl preAuthPlane = "control"
	preAuthScreen  preAuthPlane = "screen"

	// Leave most of the default global capacity available to other source IPs
	// while allowing several concurrent users behind one NAT.
	maxPreAuthConnectionsPerIP = 8
)

func (s *Server) beginPreAuth(conn net.Conn, plane preAuthPlane) bool {
	s.preAuthMu.Lock()
	defer s.preAuthMu.Unlock()
	if _, ok := s.acceptedConns[conn]; ok {
		return true
	}
	admissionKey := authRateLimitKey(conn.RemoteAddr())
	if s.ctx.Err() != nil ||
		s.preAuthCount[plane] >= s.cfg.MaxPreAuthConnections ||
		s.preAuthByIP[plane][admissionKey] >= maxPreAuthConnectionsPerIP {
		return false
	}
	if s.preAuthByIP[plane] == nil {
		s.preAuthByIP[plane] = make(map[string]int)
	}
	s.acceptedConns[conn] = trackedConn{plane: plane, preAuth: true, admissionKey: admissionKey}
	s.preAuthCount[plane]++
	s.preAuthByIP[plane][admissionKey]++
	return true
}

func (s *Server) releasePreAuth(tracked trackedConn) {
	s.preAuthCount[tracked.plane]--
	s.preAuthByIP[tracked.plane][tracked.admissionKey]--
	if s.preAuthByIP[tracked.plane][tracked.admissionKey] == 0 {
		delete(s.preAuthByIP[tracked.plane], tracked.admissionKey)
	}
}

func (s *Server) finishPreAuth(conn net.Conn) {
	s.preAuthMu.Lock()
	if tracked, ok := s.acceptedConns[conn]; ok && tracked.preAuth {
		tracked.preAuth = false
		s.acceptedConns[conn] = tracked
		s.releasePreAuth(tracked)
	}
	s.preAuthMu.Unlock()
}

func (s *Server) forgetAcceptedConn(conn net.Conn) {
	s.preAuthMu.Lock()
	if tracked, ok := s.acceptedConns[conn]; ok {
		delete(s.acceptedConns, conn)
		if tracked.preAuth {
			s.releasePreAuth(tracked)
		}
	}
	s.preAuthMu.Unlock()
}

func (s *Server) closeAcceptedConns() {
	s.preAuthMu.Lock()
	conns := make([]net.Conn, 0, len(s.acceptedConns))
	for conn := range s.acceptedConns {
		conns = append(conns, conn)
	}
	s.acceptedConns = make(map[net.Conn]trackedConn)
	s.preAuthCount = make(map[preAuthPlane]int)
	s.preAuthByIP = make(map[preAuthPlane]map[string]int)
	s.preAuthMu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

const screenWriteTimeout = 2 * time.Second

type screenClientConn struct {
	conn      net.Conn
	outbound  chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (c *screenClientConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *screenClientConn) enqueue(frame []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.outbound <- frame:
		return true
	default:
	}
	select {
	case <-c.outbound:
	default:
	}
	select {
	case c.outbound <- frame:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (s *Server) setScreenConn(sessionID uint32, conn net.Conn) *screenClientConn {
	client := &screenClientConn{
		conn:     conn,
		outbound: make(chan []byte, 1),
		done:     make(chan struct{}),
	}
	s.screenMu.Lock()
	old := s.screenConns[sessionID]
	s.screenConns[sessionID] = client
	s.screenMu.Unlock()
	if old != nil {
		old.close()
	}
	if !s.startWorker(func() { s.writeScreenPackets(sessionID, client) }) {
		s.removeScreenConn(sessionID, client)
	}
	return client
}

func (s *Server) writeScreenPackets(sessionID uint32, client *screenClientConn) {
	for {
		select {
		case frame := <-client.outbound:
			if err := client.conn.SetWriteDeadline(time.Now().Add(screenWriteTimeout)); err != nil {
				slog.Error("set screen write deadline", "session", sessionID, "err", err)
				s.removeScreenConn(sessionID, client)
				return
			}
			if err := protocol.WriteScreenPacketFrame(client.conn, frame); err != nil {
				slog.Warn("screen write failed", "session", sessionID, "err", err)
				s.removeScreenConn(sessionID, client)
				return
			}
		case <-client.done:
			return
		}
	}
}

func (s *Server) removeScreenConn(sessionID uint32, client *screenClientConn) {
	s.screenMu.Lock()
	current := s.screenConns[sessionID]
	if current == client {
		delete(s.screenConns, sessionID)
	}
	s.screenMu.Unlock()
	client.close()
}

func (s *Server) sendScreenPacketToSession(sessionID uint32, pkt *protocol.ScreenPacket) bool {
	frame, err := protocol.MarshalScreenPacketFrame(pkt)
	if err != nil {
		slog.Error("marshal screen packet", "session", sessionID, "err", err)
		return false
	}
	return s.sendScreenFrameToSession(sessionID, frame)
}

func (s *Server) sendScreenFrameToSession(sessionID uint32, frame []byte) bool {
	s.screenMu.RLock()
	client, ok := s.screenConns[sessionID]
	s.screenMu.RUnlock()
	if !ok {
		return false
	}
	return client.enqueue(frame)
}

func (s *Server) closeScreenConns() {
	s.screenMu.Lock()
	clients := make([]*screenClientConn, 0, len(s.screenConns))
	for _, client := range s.screenConns {
		clients = append(clients, client)
	}
	s.screenConns = make(map[uint32]*screenClientConn)
	s.screenMu.Unlock()
	for _, client := range clients {
		client.close()
	}
}

// Channels returns the channel manager.
func (s *Server) Channels() *ChannelManager {
	return s.channels
}

// Sessions returns the session manager.
func (s *Server) Sessions() *SessionManager {
	return s.sessions
}

// Metrics returns the server metrics.
func (s *Server) Metrics() *Metrics {
	return s.metrics
}
