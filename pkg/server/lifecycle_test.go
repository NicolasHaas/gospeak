package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func listenTCP(t *testing.T, addr string) net.Listener {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("Listen on %s: %v", addr, err)
	}
	return listener
}

func dialTCP(addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", addr)
}

func waitForStopping(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		srv.workerMu.Lock()
		stopping := srv.stopping
		srv.workerMu.Unlock()
		if stopping {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Shutdown did not enter stopping state")
		}
		time.Sleep(time.Millisecond)
	}
}

func concurrentStartResults(start func() error) [2]error {
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var results [2]error
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			results[i] = start()
		}()
	}
	close(gate)
	wg.Wait()
	return results
}

func requireSingleSuccessfulStart(t *testing.T, plane string, results [2]error) {
	t.Helper()
	successes := 0
	rejections := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if strings.Contains(err.Error(), "already started") {
			rejections++
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("concurrent %s starts returned %v; want one success and one already-started error", plane, results)
	}
}

func TestRejectedControlConnectionDoesNotLeakActiveMetric(t *testing.T) {
	srv, st, _ := newTestServer(t)
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})

	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()

	if err := clientConn.Close(); err != nil {
		t.Fatalf("Close client connection: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return")
	}

	if got := srv.metrics.ActiveConnections.Load(); got != 0 {
		t.Fatalf("ActiveConnections = %d after rejected connection, want 0", got)
	}
	if got := srv.metrics.TotalConnections.Load(); got != 1 {
		t.Fatalf("TotalConnections = %d, want 1", got)
	}
}

func TestRunCleansUpControlListenerWhenVoiceStartFails(t *testing.T) {
	voiceBlocker, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("Reserve voice address: %v", err)
	}
	defer func() { _ = voiceBlocker.Close() }()

	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.VoiceAddr = voiceBlocker.LocalAddr().String()
	cfg.MetricsAddr = ""
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	defer srv.Shutdown()

	err = srv.Run()
	if err == nil || !strings.Contains(err.Error(), "listen voice") {
		t.Fatalf("Run error = %v, want voice listener failure", err)
	}
	if srv.ctx.Err() == nil {
		t.Fatal("server context remains active after partial startup failure")
	}
	if srv.controlConn == nil {
		t.Fatal("control listener was not started before voice failure")
	}

	controlListener := listenTCP(t, srv.controlConn.Addr().String())
	_ = controlListener.Close()
}

func TestStartMetricsHTTPReportsBindFailure(t *testing.T) {
	blocker := listenTCP(t, "127.0.0.1:0")
	defer func() { _ = blocker.Close() }()

	cfg := DefaultConfig()
	cfg.MetricsAddr = blocker.Addr().String()
	srv := New(cfg, Dependencies{})
	defer srv.Shutdown()

	if err := srv.startMetricsHTTP(); err == nil {
		t.Fatal("StartMetricsHTTP succeeded with an occupied address")
	}
}

func TestShutdownClosesMetricsListenerAndIsIdempotent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MetricsAddr = "127.0.0.1:0"
	srv := New(cfg, Dependencies{})

	if err := srv.startMetricsHTTP(); err != nil {
		t.Fatalf("StartMetricsHTTP: %v", err)
	}
	srv.metricsMu.Lock()
	addr := srv.metricsConn.Addr().String()
	srv.metricsMu.Unlock()

	conn, err := dialTCP(addr, time.Second)
	if err != nil {
		t.Fatalf("Dial metrics listener: %v", err)
	}
	_ = conn.Close()

	srv.Shutdown()
	srv.Shutdown()
	metricsListener := listenTCP(t, addr)
	_ = metricsListener.Close()
}

func TestRunReportsMetricsFailureAndCleansUpListeners(t *testing.T) {
	metricsBlocker := listenTCP(t, "127.0.0.1:0")
	defer func() { _ = metricsBlocker.Close() }()

	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.VoiceAddr = "127.0.0.1:0"
	cfg.ScreenAddr = "127.0.0.1:0"
	cfg.MetricsAddr = metricsBlocker.Addr().String()
	cfg.EnableScreenShare = true
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	err = srv.Run()
	if err == nil || !strings.Contains(err.Error(), "listen metrics") {
		t.Fatalf("Run error = %v, want metrics listener failure", err)
	}
	if srv.ctx.Err() == nil {
		t.Fatal("server context remains active after metrics startup failure")
	}
	if srv.controlConn == nil || srv.voiceConn == nil || srv.screenConn == nil {
		t.Fatal("control, voice, and screen listeners must start before the metrics failure")
	}

	controlListener := listenTCP(t, srv.controlConn.Addr().String())
	_ = controlListener.Close()
	voiceAddr, ok := srv.voiceConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("voice listener address has type %T", srv.voiceConn.LocalAddr())
	}
	voiceListener, err := net.ListenUDP("udp", voiceAddr)
	if err != nil {
		t.Fatalf("Voice address remains occupied after metrics startup failure: %v", err)
	}
	_ = voiceListener.Close()
	screenListener := listenTCP(t, srv.screenConn.Addr().String())
	_ = screenListener.Close()
}

func TestShutdownStopsRunAndReleasesListeners(t *testing.T) {
	controlProbe := listenTCP(t, "127.0.0.1:0")
	controlAddr := controlProbe.Addr().String()
	_ = controlProbe.Close()
	screenProbe := listenTCP(t, "127.0.0.1:0")
	screenAddr := screenProbe.Addr().String()
	_ = screenProbe.Close()
	metricsProbe := listenTCP(t, "127.0.0.1:0")
	metricsAddr := metricsProbe.Addr().String()
	_ = metricsProbe.Close()

	voiceProbe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("Reserve voice address: %v", err)
	}
	voiceAddr := voiceProbe.LocalAddr().(*net.UDPAddr)
	_ = voiceProbe.Close()

	cfg := DefaultConfig()
	cfg.ControlAddr = controlAddr
	cfg.VoiceAddr = voiceAddr.String()
	cfg.ScreenAddr = screenAddr
	cfg.MetricsAddr = metricsAddr
	cfg.EnableScreenShare = true
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		controlConn, controlErr := dialTCP(controlAddr, 20*time.Millisecond)
		if controlErr == nil {
			_ = controlConn.Close()
		}
		screenConn, screenErr := dialTCP(screenAddr, 20*time.Millisecond)
		if screenErr == nil {
			_ = screenConn.Close()
		}
		metricsConn, metricsErr := dialTCP(metricsAddr, 20*time.Millisecond)
		if metricsErr == nil {
			_ = metricsConn.Close()
		}
		voiceListener, voiceErr := net.ListenUDP("udp", voiceAddr)
		if voiceErr == nil {
			_ = voiceListener.Close()
		}
		if controlErr == nil && screenErr == nil && metricsErr == nil && voiceErr != nil {
			break
		}
		if time.Now().After(deadline) {
			srv.Shutdown()
			t.Fatal("server listeners did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv.Shutdown()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned after Shutdown with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after Shutdown")
	}

	for name, addr := range map[string]string{
		"control": controlAddr,
		"screen":  screenAddr,
		"metrics": metricsAddr,
	} {
		listener := listenTCP(t, addr)
		if err := listener.Close(); err != nil {
			t.Errorf("Close rebound %s listener: %v", name, err)
		}
	}
	voiceListener, err := net.ListenUDP("udp", voiceAddr)
	if err != nil {
		t.Fatalf("Voice address remains occupied after Shutdown: %v", err)
	}
	_ = voiceListener.Close()
}

func TestAuthenticatedControlConnectionBalancesMetrics(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})

	go func() {
		srv.handleControlConn(handler, serverConn, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "lifecycle-user"},
	}); err != nil {
		t.Fatalf("Write AuthRequest: %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil {
		t.Fatalf("Read AuthResponse: %v", err)
	}
	if response.AuthResponse == nil || response.AuthResponse.SessionID == 0 {
		t.Fatalf("AuthResponse = %#v, want assigned session", response.AuthResponse)
	}
	if err := clientConn.Close(); err != nil {
		t.Fatalf("Close client connection: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authenticated control handler did not return")
	}
	srv.Shutdown()

	if got := srv.metrics.TotalConnections.Load(); got != 1 {
		t.Fatalf("TotalConnections = %d, want 1", got)
	}
	if got := srv.metrics.ActiveConnections.Load(); got != 0 {
		t.Fatalf("ActiveConnections = %d, want 0", got)
	}
	if got := srv.metrics.TotalDisconnects.Load(); got != 1 {
		t.Fatalf("TotalDisconnects = %d, want 1", got)
	}
}

func TestRunCleansUpControlAndVoiceWhenScreenStartFails(t *testing.T) {
	screenBlocker := listenTCP(t, "127.0.0.1:0")
	defer func() { _ = screenBlocker.Close() }()

	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.VoiceAddr = "127.0.0.1:0"
	cfg.ScreenAddr = screenBlocker.Addr().String()
	cfg.MetricsAddr = ""
	cfg.EnableScreenShare = true
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	err = srv.Run()
	if err == nil || !strings.Contains(err.Error(), "listen screen") {
		t.Fatalf("Run error = %v, want screen listener failure", err)
	}
	if srv.ctx.Err() == nil {
		t.Fatal("server context remains active after screen startup failure")
	}
	if srv.controlConn == nil || srv.voiceConn == nil {
		t.Fatal("control and voice listeners must start before the screen failure")
	}

	controlListener := listenTCP(t, srv.controlConn.Addr().String())
	_ = controlListener.Close()
	voiceAddr, ok := srv.voiceConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("voice listener address has type %T", srv.voiceConn.LocalAddr())
	}
	voiceListener, err := net.ListenUDP("udp", voiceAddr)
	if err != nil {
		t.Fatalf("Voice address remains occupied after screen startup failure: %v", err)
	}
	_ = voiceListener.Close()
}

func TestShutdownWaitsForServerWorkers(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	started := make(chan struct{})
	release := make(chan struct{})
	if !srv.startWorker(func() {
		close(started)
		<-release
	}) {
		t.Fatal("startWorker rejected worker before shutdown")
	}
	<-started

	shutdownDone := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(shutdownDone)
	}()
	waitForStopping(t, srv)
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before server worker stopped")
	default:
	}

	close(release)
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after server worker stopped")
	}
	if srv.startWorker(func() {}) {
		t.Fatal("startWorker accepted worker after shutdown")
	}
}

func TestShutdownWaitsForInFlightStartupTask(t *testing.T) {
	probe := listenTCP(t, "127.0.0.1:0")
	addr := probe.Addr().String()
	_ = probe.Close()

	srv := New(DefaultConfig(), Dependencies{})
	if !srv.beginTask() {
		t.Fatal("beginTask rejected startup task before shutdown")
	}

	shutdownDone := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(shutdownDone)
	}()
	waitForStopping(t, srv)

	listener := listenTCP(t, addr)
	select {
	case <-shutdownDone:
		_ = listener.Close()
		srv.endTask()
		t.Fatal("Shutdown returned while an admitted startup task owned a listener")
	default:
	}
	_ = listener.Close()
	srv.endTask()

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after startup task released its listener")
	}
	rebound := listenTCP(t, addr)
	_ = rebound.Close()
}

func TestShutdownWaitsForMetricsHandler(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	entered := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	handler := srv.trackHTTPHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
		close(handlerDone)
	}()
	<-entered

	shutdownDone := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(shutdownDone)
	}()
	waitForStopping(t, srv)
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before metrics handler stopped")
	default:
	}

	close(release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("metrics handler did not return")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after metrics handler stopped")
	}
}

func TestConcurrentDuplicatePlaneStartsDoNotLeak(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.VoiceAddr = "127.0.0.1:0"
	cfg.ScreenAddr = "127.0.0.1:0"
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	defer srv.Shutdown()
	requireSingleSuccessfulStart(t, "control", concurrentStartResults(func() error {
		return srv.StartControl(st)
	}))
	requireSingleSuccessfulStart(t, "voice", concurrentStartResults(srv.StartVoice))
	requireSingleSuccessfulStart(t, "screen", concurrentStartResults(srv.StartScreen))

	srv.listenerMu.Lock()
	controlAddr := srv.controlConn.Addr().String()
	voiceAddr, ok := srv.voiceConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		srv.listenerMu.Unlock()
		t.Fatalf("voice listener address has type %T", srv.voiceConn.LocalAddr())
	}
	screenAddr := srv.screenConn.Addr().String()
	srv.listenerMu.Unlock()

	shutdownDone := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked after concurrent duplicate starts")
	}

	controlListener := listenTCP(t, controlAddr)
	_ = controlListener.Close()
	voiceListener, err := net.ListenUDP("udp", voiceAddr)
	if err != nil {
		t.Fatalf("Voice address remains occupied after duplicate starts: %v", err)
	}
	_ = voiceListener.Close()
	screenListener := listenTCP(t, screenAddr)
	_ = screenListener.Close()
}

func TestShutdownBeforeRunDoesNotLeakControlListener(t *testing.T) {
	controlProbe := listenTCP(t, "127.0.0.1:0")
	controlAddr := controlProbe.Addr().String()
	_ = controlProbe.Close()

	cfg := DefaultConfig()
	cfg.ControlAddr = controlAddr
	cfg.VoiceAddr = "127.0.0.1:0"
	cfg.MetricsAddr = ""
	cfg.DataDir = t.TempDir()
	cfg.DBPath = cfg.DataDir + "/gospeak.db"

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("Create datastore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	srv := New(cfg, Dependencies{Store: st})
	srv.Shutdown()
	if err := srv.Run(); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Run error after prior Shutdown = %v, want context cancellation", err)
	}

	controlListener := listenTCP(t, controlAddr)
	_ = controlListener.Close()
}
