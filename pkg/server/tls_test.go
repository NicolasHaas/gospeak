package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

func TestLoadOrGenerateTLSRenewsAutomaticCertificateWithoutRotatingIdentity(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
	}{
		{
			name:      "near expiry",
			notBefore: now.Add(-24 * time.Hour),
			notAfter:  now.Add(24 * time.Hour),
		},
		{
			name:      "renewal boundary",
			notBefore: now.Add(-24 * time.Hour),
			notAfter:  now.Add(automaticCertRenewBefore),
		},
		{
			name:      "expired",
			notBefore: now.Add(-48 * time.Hour),
			notAfter:  now.Add(-24 * time.Hour),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultConfig()
			cfg.DataDir = dir
			original, err := loadOrGenerateTLSAt(cfg, now.Add(-100*24*time.Hour))
			if err != nil {
				t.Fatalf("generate automatic pair: %v", err)
			}
			originalLeaf := mustParseLeaf(t, original)
			certPath := filepath.Join(dir, generatedCertName)
			keyPath := filepath.Join(dir, generatedKeyName)
			keyBefore := mustReadFile(t, keyPath)
			writeSelfSignedCertificate(t, certPath, keyBefore, tc.notBefore, tc.notAfter)
			staleCert := mustReadFile(t, certPath)

			renewed, err := loadOrGenerateTLSAt(cfg, now)
			if err != nil {
				t.Fatalf("renew automatic pair: %v", err)
			}
			renewedLeaf := mustParseLeaf(t, renewed)
			if bytes.Equal(staleCert, mustReadFile(t, certPath)) {
				t.Fatal("automatic certificate was not renewed")
			}
			if !bytes.Equal(originalLeaf.RawSubjectPublicKeyInfo, renewedLeaf.RawSubjectPublicKeyInfo) {
				t.Fatal("automatic renewal rotated the pinned server identity")
			}
			if renewedLeaf.NotAfter.Sub(now) < 300*24*time.Hour {
				t.Fatalf("renewed certificate expires too soon: %v", renewedLeaf.NotAfter)
			}
			requireFileEquals(t, keyPath, keyBefore)
			requireTrustedSelfSignedHandshakeAt(t, renewed, now)
		})
	}
}

func TestTLSCertificateSourceRenewsWhileServerIsRunning(t *testing.T) {
	startedAt := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	renewAt := startedAt.Add(340 * 24 * time.Hour)
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	source, err := newTLSCertificateSourceAt(cfg, startedAt)
	if err != nil {
		t.Fatalf("create TLS certificate source: %v", err)
	}
	original := source.cert
	originalLeaf := mustParseLeaf(t, *original)
	keyPath := filepath.Join(dir, generatedKeyName)
	keyBefore := mustReadFile(t, keyPath)

	renewed, err := source.getCertificateAt(renewAt)
	if err != nil {
		t.Fatalf("get certificate in renewal window: %v", err)
	}
	if bytes.Equal(original.Certificate[0], renewed.Certificate[0]) {
		t.Fatal("running certificate source kept the expiring certificate")
	}
	renewedLeaf := mustParseLeaf(t, *renewed)
	if !bytes.Equal(originalLeaf.RawSubjectPublicKeyInfo, renewedLeaf.RawSubjectPublicKeyInfo) {
		t.Fatal("running renewal rotated the pinned server identity")
	}
	requireFileEquals(t, keyPath, keyBefore)
	requireTrustedSelfSignedHandshakeAt(t, *renewed, renewAt)

	reloaded, err := source.getCertificateAt(renewAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("get renewed certificate: %v", err)
	}
	if reloaded != renewed {
		t.Fatal("certificate source did not cache the renewed certificate")
	}
}

func TestTLSCertificateSourceRejectsCustomCertificateAfterExpiry(t *testing.T) {
	startedAt := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	autoCfg := DefaultConfig()
	autoCfg.DataDir = dir
	if _, err := loadOrGenerateTLSAt(autoCfg, startedAt); err != nil {
		t.Fatalf("generate custom source pair: %v", err)
	}
	certPath := filepath.Join(dir, generatedCertName)
	keyPath := filepath.Join(dir, generatedKeyName)
	keyBefore := mustReadFile(t, keyPath)
	writeSelfSignedCertificate(t, certPath, keyBefore, startedAt.Add(-time.Hour), startedAt.Add(time.Hour))
	cfg := DefaultConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	source, err := newTLSCertificateSourceAt(cfg, startedAt)
	if err != nil {
		t.Fatalf("create custom TLS certificate source: %v", err)
	}

	if _, err := source.getCertificateAt(startedAt.Add(2 * time.Hour)); err == nil {
		t.Fatal("running certificate source served an expired custom certificate")
	}
}

func TestTLSCertificateSourceServesValidCachedCertificateWhenRenewalFails(t *testing.T) {
	startedAt := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	source, err := newTLSCertificateSourceAt(cfg, startedAt)
	if err != nil {
		t.Fatalf("create TLS certificate source: %v", err)
	}
	cached := source.cert
	wantErr := errors.New("injected renewal failure")
	source.load = func(Config, time.Time) (tls.Certificate, error) {
		return tls.Certificate{}, wantErr
	}

	got, err := source.getCertificateAt(startedAt.Add(340 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("serve valid cached certificate: %v", err)
	}
	if got != cached {
		t.Fatal("renewal failure replaced the valid cached certificate")
	}
	if _, err := source.getCertificateAt(startedAt.Add(366 * 24 * time.Hour)); !errors.Is(err, wantErr) {
		t.Fatalf("expired cached certificate error = %v, want %v", err, wantErr)
	}
}

func TestTLSCertificateSourcesRenewConcurrently(t *testing.T) {
	startedAt := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	renewAt := startedAt.Add(340 * 24 * time.Hour)
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	first, err := newTLSCertificateSourceAt(cfg, startedAt)
	if err != nil {
		t.Fatalf("create first TLS certificate source: %v", err)
	}
	second, err := newTLSCertificateSourceAt(cfg, startedAt)
	if err != nil {
		t.Fatalf("create second TLS certificate source: %v", err)
	}
	originalLeaf := mustParseLeaf(t, *first.cert)
	keyPath := filepath.Join(dir, generatedKeyName)
	keyBefore := mustReadFile(t, keyPath)

	start := make(chan struct{})
	results := make(chan *tls.Certificate, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for _, source := range []*tlsCertificateSource{first, second} {
		wg.Add(1)
		go func(source *tlsCertificateSource) {
			defer wg.Done()
			<-start
			certificate, err := source.getCertificateAt(renewAt)
			results <- certificate
			errorsCh <- err
		}(source)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent renewal: %v", err)
		}
	}
	for certificate := range results {
		leaf := mustParseLeaf(t, *certificate)
		if !bytes.Equal(originalLeaf.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
			t.Fatal("concurrent renewal rotated the pinned server identity")
		}
		requireTrustedSelfSignedHandshakeAt(t, *certificate, renewAt)
	}
	requireFileEquals(t, keyPath, keyBefore)
	final, err := loadOrGenerateTLSAt(cfg, renewAt)
	if err != nil {
		t.Fatalf("load final concurrently renewed pair: %v", err)
	}
	finalLeaf := mustParseLeaf(t, final)
	if !bytes.Equal(originalLeaf.RawSubjectPublicKeyInfo, finalLeaf.RawSubjectPublicKeyInfo) {
		t.Fatal("final concurrently renewed pair changed identity")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".server.crt.tmp-*"))
	if err != nil {
		t.Fatalf("Glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary renewal files remain: %v", matches)
	}
}

func TestLoadOrGenerateTLSHandlesCustomValidityWithoutModification(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		wantErr   bool
	}{
		{
			name:      "near expiry remains operator managed",
			notBefore: now.Add(-24 * time.Hour),
			notAfter:  now.Add(24 * time.Hour),
		},
		{
			name:      "expired fails closed",
			notBefore: now.Add(-48 * time.Hour),
			notAfter:  now.Add(-24 * time.Hour),
			wantErr:   true,
		},
		{
			name:      "not yet valid fails closed",
			notBefore: now.Add(24 * time.Hour),
			notAfter:  now.Add(366 * 24 * time.Hour),
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			autoCfg := DefaultConfig()
			autoCfg.DataDir = dir
			if _, err := loadOrGenerateTLSAt(autoCfg, now.Add(-100*24*time.Hour)); err != nil {
				t.Fatalf("generate source pair: %v", err)
			}
			certPath := filepath.Join(dir, generatedCertName)
			keyPath := filepath.Join(dir, generatedKeyName)
			keyBefore := mustReadFile(t, keyPath)
			writeSelfSignedCertificate(t, certPath, keyBefore, tc.notBefore, tc.notAfter)
			certBefore := mustReadFile(t, certPath)

			cfg := DefaultConfig()
			cfg.CertFile = certPath
			cfg.KeyFile = keyPath
			_, err := loadOrGenerateTLSAt(cfg, now)
			if tc.wantErr && err == nil {
				t.Fatal("invalid custom certificate pair succeeded")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid custom certificate pair failed: %v", err)
			}
			requireFileEquals(t, certPath, certBefore)
			requireFileEquals(t, keyPath, keyBefore)
		})
	}
}

func TestLoadOrGenerateTLSFailedRenewalPreservesAutomaticPair(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	if _, err := loadOrGenerateTLSAt(cfg, now.Add(-100*24*time.Hour)); err != nil {
		t.Fatalf("generate automatic pair: %v", err)
	}
	certPath := filepath.Join(dir, generatedCertName)
	keyPath := filepath.Join(dir, generatedKeyName)
	keyBefore := mustReadFile(t, keyPath)
	writeSelfSignedCertificate(t, certPath, keyBefore, now.Add(-24*time.Hour), now.Add(24*time.Hour))
	certBefore := mustReadFile(t, certPath)
	stale, err := loadTLSKeyPair(certPath, keyPath, "automatic")
	if err != nil {
		t.Fatalf("load stale automatic pair: %v", err)
	}
	leaf := mustParseLeaf(t, stale)
	wantErr := errors.New("injected publish failure")
	if _, err := renewAutomaticTLSWithPublisher(
		certPath,
		keyPath,
		stale,
		leaf,
		now,
		func(string, []byte) error { return wantErr },
	); !errors.Is(err, wantErr) {
		t.Fatalf("renewal error = %v, want %v", err, wantErr)
	}
	requireFileEquals(t, certPath, certBefore)
	requireFileEquals(t, keyPath, keyBefore)
}

func TestReplaceTLSCertificateCleansTemporaryFileAfterPublishFailure(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, generatedCertName)
	if err := os.Mkdir(certPath, 0o700); err != nil {
		t.Fatalf("Mkdir destination: %v", err)
	}
	if err := replaceTLSCertificate(certPath, []byte("renewed certificate")); err == nil {
		t.Fatal("replacement over a directory unexpectedly succeeded")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".server.crt.tmp-*"))
	if err != nil {
		t.Fatalf("Glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary renewal files remain: %v", matches)
	}
	info, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("Stat destination: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("failed replacement modified its destination")
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

func TestLoadOrGenerateTLSRejectsForeignAutomaticPairWithoutModification(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		write func(*testing.T, string, string, time.Time)
	}{
		{name: "RSA self-signed pair", write: writeRSASelfSignedPair},
		{name: "CA-issued ECDSA pair", write: writeCAIssuedECDSAPair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, generatedCertName)
			keyPath := filepath.Join(dir, generatedKeyName)
			tc.write(t, certPath, keyPath, now)
			certBefore := mustReadFile(t, certPath)
			keyBefore := mustReadFile(t, keyPath)
			cfg := DefaultConfig()
			cfg.DataDir = dir

			if _, err := loadOrGenerateTLSAt(cfg, now); err == nil {
				t.Fatal("foreign automatic pair succeeded")
			}
			requireFileEquals(t, certPath, certBefore)
			requireFileEquals(t, keyPath, keyBefore)
		})
	}
}

func TestLoadOrGenerateTLSPreservesInvalidCompleteAutoPair(t *testing.T) {
	now := time.Date(2032, time.March, 14, 12, 0, 0, 0, time.UTC)

	t.Run("malformed key", func(t *testing.T) {
		dir := t.TempDir()
		cfg := DefaultConfig()
		cfg.DataDir = dir
		if _, err := loadOrGenerateTLSAt(cfg, now); err != nil {
			t.Fatalf("generate automatic pair: %v", err)
		}
		certPath := filepath.Join(dir, generatedCertName)
		keyPath := filepath.Join(dir, generatedKeyName)
		certBefore := mustReadFile(t, certPath)
		keyBefore := []byte("broken existing automatic key\n")
		if err := os.WriteFile(keyPath, keyBefore, 0o600); err != nil {
			t.Fatalf("WriteFile key: %v", err)
		}

		if _, err := loadOrGenerateTLSAt(cfg, now); err == nil {
			t.Fatal("malformed automatic pair succeeded")
		}
		requireFileEquals(t, certPath, certBefore)
		requireFileEquals(t, keyPath, keyBefore)
	})

	t.Run("not yet valid", func(t *testing.T) {
		dir := t.TempDir()
		cfg := DefaultConfig()
		cfg.DataDir = dir
		if _, err := loadOrGenerateTLSAt(cfg, now); err != nil {
			t.Fatalf("generate automatic pair: %v", err)
		}
		certPath := filepath.Join(dir, generatedCertName)
		keyPath := filepath.Join(dir, generatedKeyName)
		keyBefore := mustReadFile(t, keyPath)
		writeSelfSignedCertificate(t, certPath, keyBefore, now.Add(24*time.Hour), now.Add(366*24*time.Hour))
		certBefore := mustReadFile(t, certPath)

		if _, err := loadOrGenerateTLSAt(cfg, now); err == nil {
			t.Fatal("not-yet-valid automatic certificate succeeded")
		}
		requireFileEquals(t, certPath, certBefore)
		requireFileEquals(t, keyPath, keyBefore)
	})
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // tests pass only paths inside t.TempDir
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func mustParseLeaf(t *testing.T, certificate tls.Certificate) *x509.Certificate {
	t.Helper()
	if len(certificate.Certificate) == 0 {
		t.Fatal("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return leaf
}

func writeRSASelfSignedPair(t *testing.T, certPath, keyPath string, now time.Time) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := testServerCertificateTemplate(t, now)
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	writePEMFile(t, certPath, "CERTIFICATE", der, 0o600)
	writePEMFile(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(privateKey), 0o600)
}

func writeCAIssuedECDSAPair(t *testing.T, certPath, keyPath string, now time.Time) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey CA: %v", err)
	}
	caTemplate := testServerCertificateTemplate(t, now)
	caTemplate.IsCA = true
	caTemplate.BasicConstraintsValid = true
	caTemplate.KeyUsage = x509.KeyUsageCertSign
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate CA: %v", err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("ParseCertificate CA: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey leaf: %v", err)
	}
	leafTemplate := testServerCertificateTemplate(t, now)
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate leaf: %v", err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	writePEMFile(t, certPath, "CERTIFICATE", leafDER, 0o600)
	writePEMFile(t, keyPath, "EC PRIVATE KEY", privateKeyDER, 0o600)
}

func testServerCertificateTemplate(t *testing.T, now time.Time) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Foreign TLS Material"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(300 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
}

func writePEMFile(t *testing.T, path, blockType string, der []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func writeSelfSignedCertificate(t *testing.T, certPath string, keyPEM []byte, notBefore, notAfter time.Time) {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("Decode private key PEM: no block")
	}
	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParseECPrivateKey: %v", err)
	}
	writeCertificateForKey(t, certPath, privateKey, notBefore, notAfter)
}

func writeCertificateForKey(t *testing.T, certPath string, privateKey *ecdsa.PrivateKey, notBefore, notAfter time.Time) {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"GoSpeak Server"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile certificate: %v", err)
	}
}

func requireFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := mustReadFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("file %s was modified", path)
	}
}

func requireTrustedSelfSignedHandshake(t *testing.T, certificate tls.Certificate) {
	requireTrustedSelfSignedHandshakeAt(t, certificate, time.Now())
}

func requireTrustedSelfSignedHandshakeAt(t *testing.T, certificate tls.Certificate, now time.Time) {
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
		Time:       func() time.Time { return now },
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
