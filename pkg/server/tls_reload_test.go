package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestTLSConfiguredReloadSameKey(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	cfg := configuredReloadFixture(t, now)
	source, err := newTLSCertificateSourceAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	old := source.cert
	key := mustReadFile(t, cfg.KeyFile)
	writeSelfSignedCertificate(t, cfg.CertFile, key, now.Add(-time.Hour), now.Add(24*time.Hour))
	replacementPEM := mustReadFile(t, cfg.CertFile)
	modes := make(map[string]os.FileMode)
	for _, path := range []string{cfg.CertFile, cfg.KeyFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		modes[path] = info.Mode()
	}
	replacement, err := loadOrGenerateTLSAt(cfg, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("fresh loader rejects replacement: %v", err)
	}
	got, err := source.getCertificateAt(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got.Certificate[0], old.Certificate[0]) {
		t.Fatal("running configured source kept old certificate after reload deadline")
	}
	if !bytes.Equal(got.Certificate[0], replacement.Certificate[0]) {
		t.Fatal("source did not publish replacement")
	}
	if !bytes.Equal(mustParseLeaf(t, *got).RawSubjectPublicKeyInfo, mustParseLeaf(t, *old).RawSubjectPublicKeyInfo) {
		t.Fatal("same-key renewal changed identity")
	}
	got, err = source.getCertificateAt(now.Add(2 * time.Hour))
	if err != nil || got == nil {
		t.Fatalf("replacement unavailable after old expiry: %v", err)
	}
	requireTrustedSelfSignedHandshakeAt(t, *got, now.Add(2*time.Hour))
	requireFileEquals(t, cfg.KeyFile, key)
	requireFileEquals(t, cfg.CertFile, replacementPEM)
	for path, mode := range modes {
		info, err := os.Stat(path)
		if err != nil || info.Mode() != mode {
			t.Fatalf("successful reload changed operator file mode: %s (%v)", path, err)
		}
	}
}

func TestTLSConfiguredReloadPollBackoffAndValidity(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	cfg := configuredReloadFixture(t, now)
	// Expiry inside a retry interval must not extend the cache's validity.
	writeSelfSignedCertificate(t, cfg.CertFile, mustReadFile(t, cfg.KeyFile), now.Add(-time.Hour), now.Add(90*time.Second))
	source, err := newTLSCertificateSourceAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	cached := source.cert
	attempts := 0
	failure := errors.New("operator pair unavailable")
	source.load = func(Config, time.Time) (tls.Certificate, error) { attempts++; return tls.Certificate{}, failure }
	for _, offset := range []time.Duration{0, time.Minute - time.Nanosecond} {
		if got, err := source.getCertificateAt(now.Add(offset)); err != nil || got != cached || attempts != 0 {
			t.Fatalf("before poll %s: got=%p err=%v attempts=%d", offset, got, err, attempts)
		}
	}
	for _, offset := range []time.Duration{time.Minute, 90 * time.Second, 2*time.Minute - time.Nanosecond} {
		got, err := source.getCertificateAt(now.Add(offset))
		valid := offset <= 90*time.Second
		if valid && (err != nil || got != cached) {
			t.Fatalf("valid fallback: %p %v", got, err)
		}
		if !valid && (got != nil || !errors.Is(err, failure)) {
			t.Fatalf("invalid fallback: %p %v", got, err)
		}
		if attempts != 1 {
			t.Fatalf("backoff attempts=%d", attempts)
		}
	}
	writeSelfSignedCertificate(t, cfg.CertFile, mustReadFile(t, cfg.KeyFile), now.Add(-time.Hour), now.Add(24*time.Hour))
	source.load = func(cfg Config, at time.Time) (tls.Certificate, error) {
		attempts++
		return loadOrGenerateTLSAt(cfg, at)
	}
	recovered, err := source.getCertificateAt(now.Add(2 * time.Minute))
	if err != nil || recovered == cached || attempts != 2 || source.renewErr != nil {
		t.Fatalf("recovery: %p %v attempts=%d", recovered, err, attempts)
	}
	if _, err := source.getCertificateAt(now.Add(3*time.Minute - time.Nanosecond)); err != nil || attempts != 2 {
		t.Fatalf("success poll throttle: %v attempts=%d", err, attempts)
	}
	if _, err := source.getCertificateAt(now.Add(3 * time.Minute)); err != nil || attempts != 3 {
		t.Fatalf("success poll boundary: %v attempts=%d", err, attempts)
	}
	// A clock moving before NotBefore must not bypass validation within the poll interval.
	if got, err := source.getCertificateAt(now.Add(-2 * time.Hour)); err == nil || got != nil {
		t.Fatal("future cached leaf served")
	}
}

func TestTLSConfiguredReloadInvalidCandidatesDoNotWrite(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"missing cert", "missing key", "malformed", "mismatch", "future", "expired"} {
		t.Run(kind, func(t *testing.T) {
			cfg := configuredReloadFixture(t, now)
			source, err := newTLSCertificateSourceAt(cfg, now)
			if err != nil {
				t.Fatal(err)
			}
			cached := source.cert
			key := mustReadFile(t, cfg.KeyFile)
			switch kind {
			case "missing cert":
				err = os.Remove(cfg.CertFile)
			case "missing key":
				err = os.Remove(cfg.KeyFile)
			case "malformed":
				err = os.WriteFile(cfg.CertFile, []byte("not PEM"), 0600)
			case "mismatch":
				other := configuredReloadFixture(t, now)
				err = os.WriteFile(cfg.KeyFile, mustReadFile(t, other.KeyFile), 0600)
			case "future":
				writeSelfSignedCertificate(t, cfg.CertFile, key, now.Add(3*time.Hour), now.Add(24*time.Hour))
			case "expired":
				writeSelfSignedCertificate(t, cfg.CertFile, key, now.Add(-2*time.Hour), now.Add(-time.Hour))
			}
			if err != nil {
				t.Fatal(err)
			}
			// Snapshot the operator's resulting state, including absent paths and modes.
			before, err := os.ReadDir(cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			contents := make(map[string][]byte)
			modes := make(map[string]os.FileMode)
			for _, entry := range before {
				path := filepath.Join(cfg.DataDir, entry.Name())
				contents[path] = mustReadFile(t, path)
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				modes[path] = info.Mode()
			}
			for _, offset := range []time.Duration{time.Minute, 2 * time.Hour} {
				got, err := source.getCertificateAt(now.Add(offset))
				if offset == time.Minute && (err != nil || got != cached) {
					t.Fatalf("bad replacement lost valid cache: %p %v", got, err)
				}
				if offset == 2*time.Hour && (err == nil || got != nil) {
					t.Fatal("expired cache accepted")
				}
			}
			after, err := os.ReadDir(cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatal("reload created or removed operator files")
			}
			for path, data := range contents {
				requireFileEquals(t, path, data)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode() != modes[path] {
					t.Fatal("reload changed operator mode")
				}
			}
			// Repair complete pair and recover without recreating the source.
			if err := os.WriteFile(cfg.KeyFile, key, 0600); err != nil {
				t.Fatal(err)
			}
			writeSelfSignedCertificate(t, cfg.CertFile, key, now.Add(-time.Hour), now.Add(24*time.Hour))
			if got, err := source.getCertificateAt(now.Add(2*time.Hour + time.Minute)); err != nil || got == cached {
				t.Fatalf("repair: %p %v", got, err)
			}
		})
	}
}

func TestTLSConfiguredReloadConcurrentPublication(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	cfg := configuredReloadFixture(t, now)
	source, err := newTLSCertificateSourceAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	// A different operator key is accepted by the source, not silently trusted by clients.
	replacementCfg := configuredReloadFixture(t, now)
	replacement, err := loadOrGenerateTLSAt(replacementCfg, now)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	attempts := 0
	source.load = func(Config, time.Time) (tls.Certificate, error) {
		attempts++
		close(entered)
		<-release
		return replacement, nil
	}
	const readers = 16
	results := make(chan *tls.Certificate, readers)
	failures := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := source.getCertificateAt(now.Add(time.Minute))
			results <- got
			failures <- err
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var published *tls.Certificate
	for got := range results {
		if published == nil {
			published = got
		}
		if got != published || !bytes.Equal(got.Certificate[0], replacement.Certificate[0]) || got.PrivateKey != replacement.PrivateKey {
			t.Fatal("partial or inconsistent pair published")
		}
	}
	if attempts != 1 {
		t.Fatalf("concurrent reload attempts=%d", attempts)
	}
}

func TestTLSConfiguredReloadLogging(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	cfg := configuredReloadFixture(t, now)
	source, err := newTLSCertificateSourceAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	for i := 1; i <= 3; i++ {
		if _, err := source.getCertificateAt(now.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("unchanged polls log success: %s", output.String())
	}
	if err := os.Remove(cfg.CertFile); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{4 * time.Minute, 4*time.Minute + time.Second, 5*time.Minute - time.Nanosecond, 5 * time.Minute} {
		if _, err := source.getCertificateAt(now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(output.String(), "TLS certificate refresh failed"); got != 2 {
		t.Fatalf("failure logs=%d, want 2: %s", got, output.String())
	}
}

func TestTLSConfiguredReloadHandshakeCallback(t *testing.T) {
	now := time.Now()
	cfg := configuredReloadFixture(t, now)
	source, err := newTLSCertificateSourceAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	original := source.cert
	listener := listenTCP(t, "127.0.0.1:0")
	defer listener.Close()
	config := &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: source.getCertificate}
	results := make(chan error, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				results <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			secure := tls.Server(conn, config)
			err = secure.Handshake()
			_ = conn.Close()
			results <- err
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	handshake := func(want *tls.Certificate) {
		t.Helper()
		peer := configuredReloadDial(t, listener.Addr().String(), *want)
		if !bytes.Equal(peer.Raw, want.Certificate[0]) {
			t.Fatal("callback served wrong certificate")
		}
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	handshake(original)
	writeSelfSignedCertificate(t, cfg.CertFile, mustReadFile(t, cfg.KeyFile), now.Add(-time.Hour), now.Add(24*time.Hour))
	replacement, err := loadOrGenerateTLSAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	// Advance only the source deadline; exact interval semantics are fake-clock tested above.
	source.mu.Lock()
	source.retryAt = now.Add(-time.Minute)
	source.mu.Unlock()
	handshake(&replacement)
}

func TestTLSConfiguredPairBothPlaneCallbacks(t *testing.T) {
	cfg := configuredReloadFixture(t, time.Now())
	cfg.ControlAddr, cfg.ScreenAddr = "127.0.0.1:0", "127.0.0.1:0"
	cfg.DBPath = filepath.Join(cfg.DataDir, "gospeak.db")
	store, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	srv := New(cfg, Dependencies{Store: store})
	defer srv.Shutdown()
	if err := srv.StartControl(store); err != nil {
		t.Fatal(err)
	}
	if err := srv.StartScreen(); err != nil {
		t.Fatal(err)
	}
	cert, err := loadOrGenerateTLSAt(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range []net.Listener{srv.controlConn, srv.screenConn} {
		configuredReloadDial(t, listener.Addr().String(), cert)
	}
}

func configuredReloadDial(t *testing.T, addr string, certificate tls.Certificate) *x509.Certificate {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(mustParseLeaf(t, certificate))
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", addr, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	peer := conn.ConnectionState().PeerCertificates[0]
	_ = conn.NetConn().Close()
	return peer
}

func configuredReloadFixture(t *testing.T, now time.Time) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	if _, err := loadOrGenerateTLSAt(cfg, now); err != nil {
		t.Fatal(err)
	}
	cfg.CertFile = filepath.Join(cfg.DataDir, generatedCertName)
	cfg.KeyFile = filepath.Join(cfg.DataDir, generatedKeyName)
	writeSelfSignedCertificate(t, cfg.CertFile, mustReadFile(t, cfg.KeyFile), now.Add(-time.Hour), now.Add(time.Hour))
	return cfg
}
