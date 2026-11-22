package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestPKITrustHistoryDowngrade(t *testing.T) {
	if addr := os.Getenv("GOSPEAK_PKI_HISTORY_CHILD"); addr != "" {
		cert, _ := newTestCertificate(t, nil, nil, true, "localhost", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		cfg, err := newTLSConfig(addr, "", x509.NewCertPool())
		if err != nil {
			t.Fatal(err)
		}
		var changed *ServerIdentityChangedError
		if err := cfg.VerifyConnection(connectionState(cert)); !errors.As(err, &changed) {
			t.Fatalf("fresh process lost persisted PKI history: %v", err)
		}
		return
	}
	rootDir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", rootDir)
	case "darwin", "ios":
		t.Setenv("HOME", rootDir)
	default:
		t.Setenv("XDG_CONFIG_HOME", rootDir)
	}
	root, rootKey := newTestCertificate(t, nil, nil, true, "root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	roots := x509.NewCertPool()
	roots.AddCert(root)
	var certificate atomic.Pointer[tls.Certificate]
	var blockHistorySave atomic.Bool
	var historyPath string
	setCertificate := func(trusted bool) string {
		parent, key := root, rootKey
		if !trusted {
			parent, key = nil, nil
		}
		leaf, leafKey := newTestCertificate(t, parent, key, !trusted, "localhost", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		certificate.Store(&tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey})
		return SPKIFingerprint(leaf)
	}
	setCertificate(true)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if blockHistorySave.Load() {
			if err := os.Mkdir(historyPath, 0o700); err != nil {
				return nil, err
			}
		}
		return certificate.Load(), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("localhost", port)
	historyPath, err = pkiHistoryPath(addr)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	auth := make(chan bool, 8)
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				msg, err := protocol.ReadControlMessage(conn)
				auth <- err == nil && msg.AuthRequest != nil
			})
		}
	})
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	dial := func(pin string) (*ControlClient, error) {
		return newControlClientContext(context.Background(), addr, pin, roots)
	}
	for _, phase := range []string{"initial verified PKI", "valid PKI new key"} {
		t.Run(phase, func(t *testing.T) {
			setCertificate(true)
			c, err := dial("")
			if err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			if <-auth {
				t.Fatal("credentials sent during TLS setup")
			}
		})
	}
	// A fresh process must recover history without any in-memory state.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPKITrustHistoryDowngrade$", "-test.timeout=10s") // #nosec G204 G702 -- Re-execute only this test binary to verify persisted trust in a fresh process.
	cmd.Env = append(os.Environ(), "GOSPEAK_PKI_HISTORY_CHILD="+addr)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart: %v\n%s", err, output)
	}
	pin := setCertificate(false)
	_, err = dial("")
	var changed *ServerIdentityChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("PKI -> untrusted after reconnect: got %T, want ServerIdentityChangedError", err)
	}
	if changed.Received != pin || changed.Expected == "" {
		t.Fatal("downgrade warning lacks previous trust or received fingerprint")
	}
	if <-auth {
		t.Fatal("credentials sent before explicit trust")
	}
	// Declining leaves history unchanged, including after loading it afresh.
	_, err = dial("")
	if !errors.As(err, &changed) {
		t.Fatal("declined downgrade lost trust history")
	}
	if <-auth {
		t.Fatal("credentials sent after decline")
	}
	// An explicit one-connection pin permits auth but does not clear PKI history.
	c, err := dial(pin)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Authenticate("fixture-only", "fixture-user")
	_ = c.Close()
	if !<-auth {
		t.Fatal("explicitly trusted connection did not authenticate")
	}
	_, err = dial("")
	if !errors.As(err, &changed) {
		t.Fatal("one-connection trust erased PKI history")
	}
	if <-auth {
		t.Fatal("credentials sent without renewed trust")
	}
	// A different host or port has no invented prior history.
	for _, other := range []string{net.JoinHostPort("127.0.0.1", port), "localhost:1"} {
		cfg, err := newTLSConfig(other, "", roots)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(certificate.Load().Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		var unknown *UntrustedServerError
		if err := cfg.VerifyConnection(connectionState(leaf)); !errors.As(err, &unknown) {
			t.Fatalf("other address classified as previously trusted: %T", err)
		}
	}
	path, err := pkiHistoryPath(addr)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
	// Corrupt or unsafe history must not silently become an unknown server.
	if err := os.WriteFile(path, []byte("[invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dial(""); err == nil {
		t.Fatal("corrupt trust history accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	setCertificate(true)
	if _, err := dial(""); err == nil {
		t.Fatal("unwritable trust history accepted")
	}
	// Preflight succeeds, but persistence fails after the verified handshake.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	blockHistorySave.Store(true)
	c, err = dial("")
	if err == nil || c != nil {
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("connection exposed despite trust history persistence failure")
	}
	if <-auth {
		t.Fatal("credentials sent despite persistence failure")
	}
	// This feature never writes pins, bookmarks, or migrates legacy files.
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "servers.yaml")); !os.IsNotExist(err) {
		t.Fatal("PKI history changed bookmark/pin storage")
	}
}
