package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadOrGenerateTLSAutoGeneratesAndReloadsSelfSignedPair(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "tls")
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	cfg.CertFile = ""
	cfg.KeyFile = ""

	first, err := loadOrGenerateTLS(cfg)
	if err != nil {
		t.Fatalf("first loadOrGenerateTLS: %v", err)
	}
	if len(first.Certificate) == 0 {
		t.Fatal("generated certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		t.Fatalf("generated certificate does not verify its own signature: %v", err)
	}
	requireTrustedSelfSignedHandshake(t, first)

	certPath := filepath.Join(dataDir, "server.crt")
	keyPath := filepath.Join(dataDir, "server.key")
	certBefore := mustReadFile(t, certPath)
	keyBefore := mustReadFile(t, keyPath)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("Stat key: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("key permissions = %04o, want 0600", got)
		}
	}

	second, err := loadOrGenerateTLS(cfg)
	if err != nil {
		t.Fatalf("second loadOrGenerateTLS: %v", err)
	}
	if len(second.Certificate) == 0 || !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("second call replaced the existing self-signed certificate")
	}
	requireFileEquals(t, certPath, certBefore)
	requireFileEquals(t, keyPath, keyBefore)

	matches, err := filepath.Glob(filepath.Join(dataDir, ".*.tmp-*"))
	if err != nil {
		t.Fatalf("Glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary certificate files remain: %v", matches)
	}
}

func TestLoadOrGenerateTLSLoadsExplicitSelfSignedPairWithoutModification(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	autoCfg := DefaultConfig()
	autoCfg.DataDir = sourceDir
	if _, err := loadOrGenerateTLS(autoCfg); err != nil {
		t.Fatalf("generate source self-signed pair: %v", err)
	}

	certPath := filepath.Join(sourceDir, "server.crt")
	keyPath := filepath.Join(sourceDir, "server.key")
	certBefore := mustReadFile(t, certPath)
	keyBefore := mustReadFile(t, keyPath)

	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "unused-auto-dir")
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	loaded, err := loadOrGenerateTLS(cfg)
	if err != nil {
		t.Fatalf("load explicit self-signed pair: %v", err)
	}
	requireTrustedSelfSignedHandshake(t, loaded)
	requireFileEquals(t, certPath, certBefore)
	requireFileEquals(t, keyPath, keyBefore)
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("custom mode touched auto data directory: %v", err)
	}
}

func TestLoadOrGenerateTLSRejectsPartialCustomConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		certFile string
		keyFile  string
	}{
		{name: "certificate only", certFile: "custom.crt"},
		{name: "key only", keyFile: "custom.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultConfig()
			cfg.DataDir = dir
			if tc.certFile != "" {
				cfg.CertFile = filepath.Join(dir, tc.certFile)
			}
			if tc.keyFile != "" {
				cfg.KeyFile = filepath.Join(dir, tc.keyFile)
			}

			if _, err := loadOrGenerateTLS(cfg); err == nil {
				t.Fatal("partial custom certificate configuration succeeded")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial custom configuration created files: %v", entries)
			}
		})
	}
}

func TestLoadOrGenerateTLSRejectsMissingCustomPairWithoutCreatingIt(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "custom.crt")
	keyPath := filepath.Join(dir, "custom.key")
	cfg := DefaultConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath

	if _, err := loadOrGenerateTLS(cfg); err == nil {
		t.Fatal("missing custom certificate pair succeeded")
	}
	for _, path := range []string{certPath, keyPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("custom path %q was created: %v", path, err)
		}
	}
}

func TestLoadOrGenerateTLSPreservesBrokenExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "custom.crt")
	keyPath := filepath.Join(dir, "custom.key")
	certBefore := []byte("not a certificate\n")
	keyBefore := []byte("not a key\n")
	if err := os.WriteFile(certPath, certBefore, 0o600); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyBefore, 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	cfg := DefaultConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	if _, err := loadOrGenerateTLS(cfg); err == nil {
		t.Fatal("broken explicit certificate pair succeeded")
	}
	requireFileEquals(t, certPath, certBefore)
	requireFileEquals(t, keyPath, keyBefore)
}

func TestLoadOrGenerateTLSPreservesMismatchedExplicitFiles(t *testing.T) {
	firstDir := filepath.Join(t.TempDir(), "first")
	secondDir := filepath.Join(t.TempDir(), "second")
	for _, dir := range []string{firstDir, secondDir} {
		cfg := DefaultConfig()
		cfg.DataDir = dir
		if _, err := loadOrGenerateTLS(cfg); err != nil {
			t.Fatalf("generate pair in %s: %v", dir, err)
		}
	}

	certPath := filepath.Join(firstDir, "server.crt")
	keyPath := filepath.Join(secondDir, "server.key")
	certBefore := mustReadFile(t, certPath)
	keyBefore := mustReadFile(t, keyPath)
	cfg := DefaultConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath

	if _, err := loadOrGenerateTLS(cfg); err == nil {
		t.Fatal("mismatched explicit certificate pair succeeded")
	}
	requireFileEquals(t, certPath, certBefore)
	requireFileEquals(t, keyPath, keyBefore)
}

func TestLoadOrGenerateTLSPreservesBrokenAutoFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	certBefore := []byte("broken existing automatic certificate\n")
	if err := os.WriteFile(certPath, certBefore, 0o600); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}

	cfg := DefaultConfig()
	cfg.DataDir = dir
	if _, err := loadOrGenerateTLS(cfg); err == nil {
		t.Fatal("partial automatic certificate state succeeded")
	}
	requireFileEquals(t, certPath, certBefore)
	if _, err := os.Stat(filepath.Join(dir, "server.key")); !os.IsNotExist(err) {
		t.Fatalf("missing automatic key was generated: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // tests pass only paths inside t.TempDir
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func requireFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := mustReadFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("file %s was modified", path)
	}
}

func requireTrustedSelfSignedHandshake(t *testing.T, certificate tls.Certificate) {
	t.Helper()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate for handshake: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)

	serverSide, clientSide := net.Pipe()
	t.Cleanup(func() {
		_ = serverSide.Close()
		_ = clientSide.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	_ = serverSide.SetDeadline(deadline)
	_ = clientSide.SetDeadline(deadline)
	serverTLS := tls.Server(serverSide, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})
	clientTLS := tls.Client(clientSide, &tls.Config{
		RootCAs:    roots,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	serverResult := make(chan error, 1)
	go func() { serverResult <- serverTLS.HandshakeContext(ctx) }()
	if err := clientTLS.HandshakeContext(ctx); err != nil {
		t.Fatalf("client handshake with self-signed certificate: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("server handshake with self-signed certificate: %v", err)
	}
}
