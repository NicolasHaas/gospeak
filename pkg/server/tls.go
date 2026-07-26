package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	generatedCertName = "server.crt"
	generatedKeyName  = "server.key"
)

// loadOrGenerateTLS loads an explicitly configured pair, reuses an existing
// automatic pair, or generates a self-signed pair only when neither automatic
// file exists. Existing certificate material is never overwritten.
func loadOrGenerateTLS(cfg Config) (tls.Certificate, error) {
	hasCustomCert := cfg.CertFile != ""
	hasCustomKey := cfg.KeyFile != ""
	if hasCustomCert != hasCustomKey {
		return tls.Certificate{}, fmt.Errorf("TLS certificate and key must be configured together")
	}
	if hasCustomCert {
		return loadTLSKeyPair(cfg.CertFile, cfg.KeyFile, "configured")
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "."
	}
	certPath := filepath.Join(dataDir, generatedCertName)
	keyPath := filepath.Join(dataDir, generatedKeyName)
	certExists, err := pathExists(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("inspect automatic TLS certificate: %w", err)
	}
	keyExists, err := pathExists(keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("inspect automatic TLS key: %w", err)
	}
	if certExists != keyExists {
		return tls.Certificate{}, fmt.Errorf("automatic TLS certificate state is incomplete: both %q and %q must exist or neither may exist", certPath, keyPath)
	}
	if certExists {
		return loadTLSKeyPair(certPath, keyPath, "automatic")
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create TLS data directory: %w", err)
	}
	return generateSelfSignedTLS(certPath, keyPath)
}

func loadTLSKeyPair(certPath, keyPath, source string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load %s TLS certificate %q and key %q: %w", source, certPath, keyPath, err)
	}
	slog.Info("loaded TLS certificate", "cert", certPath, "source", source)
	return cert, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func generateSelfSignedTLS(certPath, keyPath string) (tls.Certificate, error) {
	slog.Info("generating self-signed TLS certificate")
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate TLS key: %w", err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate TLS certificate serial: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{Organization: []string{"GoSpeak Server"}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create TLS certificate: %w", err)
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal TLS key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("validate generated TLS pair: %w", err)
	}
	if err := publishTLSKeyPair(certPath, keyPath, certPEM, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	slog.Info("TLS certificate generated", "cert", certPath, "key", keyPath)
	return cert, nil
}

func publishTLSKeyPair(certPath, keyPath string, certPEM, keyPEM []byte) error {
	certTemp, err := prepareTLSFile(certPath, certPEM, 0o644)
	if err != nil {
		return fmt.Errorf("prepare TLS certificate: %w", err)
	}
	defer func() { _ = os.Remove(certTemp) }()
	keyTemp, err := prepareTLSFile(keyPath, keyPEM, 0o600)
	if err != nil {
		return fmt.Errorf("prepare TLS key: %w", err)
	}
	defer func() { _ = os.Remove(keyTemp) }()

	if err := os.Link(certTemp, certPath); err != nil {
		return fmt.Errorf("publish TLS certificate without overwrite: %w", err)
	}
	if err := os.Link(keyTemp, keyPath); err != nil {
		if removeErr := os.Remove(certPath); removeErr != nil {
			return fmt.Errorf("publish TLS key without overwrite: %w; rollback certificate: %v", err, removeErr)
		}
		return fmt.Errorf("publish TLS key without overwrite: %w", err)
	}
	return nil
}

func prepareTLSFile(target string, data []byte, mode os.FileMode) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := temp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = temp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}
