package server

import (
	"bytes"
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
	"sync"
	"time"
)

const (
	generatedCertName        = "server.crt"
	generatedKeyName         = "server.key"
	automaticCertValidity    = 365 * 24 * time.Hour
	automaticCertRenewBefore = 30 * 24 * time.Hour
)

// loadOrGenerateTLS loads an explicitly configured pair, reuses or renews an
// existing automatic pair, or generates a self-signed pair when neither
// automatic file exists. Custom certificate material is never modified.
func loadOrGenerateTLS(cfg Config) (tls.Certificate, error) {
	return loadOrGenerateTLSAt(cfg, time.Now())
}

type tlsCertificateSource struct {
	mu        sync.Mutex
	cfg       Config
	automatic bool
	cert      *tls.Certificate
	leaf      *x509.Certificate
	load      func(Config, time.Time) (tls.Certificate, error)
}

func newServerTLSConfig(cfg Config) (*tls.Config, error) {
	source, err := newTLSCertificateSourceAt(cfg, time.Now())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: source.getCertificate,
	}, nil
}

func newTLSCertificateSourceAt(cfg Config, now time.Time) (*tlsCertificateSource, error) {
	cert, err := loadOrGenerateTLSAt(cfg, now)
	if err != nil {
		return nil, err
	}
	leaf, err := certificateLeaf(cert)
	if err != nil {
		return nil, fmt.Errorf("parse loaded TLS certificate: %w", err)
	}
	return &tlsCertificateSource{
		cfg:       cfg,
		automatic: cfg.CertFile == "" && cfg.KeyFile == "",
		cert:      &cert,
		leaf:      leaf,
		load:      loadOrGenerateTLSAt,
	}, nil
}

func (s *tlsCertificateSource) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return s.getCertificateAt(time.Now())
}

func (s *tlsCertificateSource) getCertificateAt(now time.Time) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.automatic {
		if now.Before(s.leaf.NotBefore) || now.After(s.leaf.NotAfter) {
			return nil, fmt.Errorf("configured TLS certificate is not valid at %s", now.Format(time.RFC3339))
		}
		return s.cert, nil
	}
	if s.leaf.NotAfter.After(now.Add(automaticCertRenewBefore)) {
		return s.cert, nil
	}
	cert, err := s.load(s.cfg, now)
	if err != nil {
		if _, validityErr := validCertificateLeaf(*s.cert, now); validityErr == nil {
			slog.Warn("automatic TLS certificate renewal failed; continuing with valid cached certificate", "err", err, "expires", s.leaf.NotAfter)
			return s.cert, nil
		}
		return nil, fmt.Errorf("renew automatic TLS certificate after cached certificate became invalid: %w", err)
	}
	leaf, err := certificateLeaf(cert)
	if err != nil {
		return nil, fmt.Errorf("parse renewed TLS certificate: %w", err)
	}
	s.cert = &cert
	s.leaf = leaf
	return s.cert, nil
}

func loadOrGenerateTLSAt(cfg Config, now time.Time) (tls.Certificate, error) {
	hasCustomCert := cfg.CertFile != ""
	hasCustomKey := cfg.KeyFile != ""
	if hasCustomCert != hasCustomKey {
		return tls.Certificate{}, fmt.Errorf("TLS certificate and key must be configured together")
	}
	if hasCustomCert {
		cert, err := loadTLSKeyPair(cfg.CertFile, cfg.KeyFile, "configured")
		if err != nil {
			return tls.Certificate{}, err
		}
		if _, err := validCertificateLeaf(cert, now); err != nil {
			return tls.Certificate{}, fmt.Errorf("validate configured TLS certificate %q: %w", cfg.CertFile, err)
		}
		return cert, nil
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
		cert, err := loadTLSKeyPair(certPath, keyPath, "automatic")
		if err != nil {
			return tls.Certificate{}, err
		}
		leaf, err := certificateLeaf(cert)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("parse automatic TLS certificate %q: %w", certPath, err)
		}
		if _, err := validateAutomaticCertificate(cert, leaf, certPath, keyPath); err != nil {
			return tls.Certificate{}, err
		}
		if now.Before(leaf.NotBefore) {
			return tls.Certificate{}, fmt.Errorf("automatic TLS certificate %q is not valid before %s", certPath, leaf.NotBefore.Format(time.RFC3339))
		}
		if leaf.NotAfter.After(now.Add(automaticCertRenewBefore)) {
			return cert, nil
		}
		return renewAutomaticTLS(certPath, keyPath, cert, leaf, now)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create TLS data directory: %w", err)
	}
	return generateSelfSignedTLS(certPath, keyPath, now)
}

func loadTLSKeyPair(certPath, keyPath, source string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load %s TLS certificate %q and key %q: %w", source, certPath, keyPath, err)
	}
	slog.Info("loaded TLS certificate", "cert", certPath, "source", source)
	return cert, nil
}

func certificateLeaf(cert tls.Certificate) (*x509.Certificate, error) {
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse leaf: %w", err)
	}
	return leaf, nil
}

func validCertificateLeaf(cert tls.Certificate, now time.Time) (*x509.Certificate, error) {
	leaf, err := certificateLeaf(cert)
	if err != nil {
		return nil, err
	}
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("certificate is not valid before %s", leaf.NotBefore.Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("certificate expired at %s", leaf.NotAfter.Format(time.RFC3339))
	}
	return leaf, nil
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

func generateSelfSignedTLS(certPath, keyPath string, now time.Time) (tls.Certificate, error) {
	slog.Info("generating self-signed TLS certificate")
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate TLS key: %w", err)
	}
	certPEM, err := selfSignedCertificatePEM(priv, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal TLS key: %w", err)
	}
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

func selfSignedCertificatePEM(priv *ecdsa.PrivateKey, now time.Time) ([]byte, error) {
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate TLS certificate serial: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{Organization: []string{"GoSpeak Server"}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(automaticCertValidity),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("create TLS certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

func renewAutomaticTLS(certPath, keyPath string, cert tls.Certificate, leaf *x509.Certificate, now time.Time) (tls.Certificate, error) {
	return renewAutomaticTLSWithPublisher(certPath, keyPath, cert, leaf, now, replaceTLSCertificate)
}

func renewAutomaticTLSWithPublisher(
	certPath, keyPath string,
	cert tls.Certificate,
	leaf *x509.Certificate,
	now time.Time,
	publish func(string, []byte) error,
) (tls.Certificate, error) {
	privateKey, err := validateAutomaticCertificate(cert, leaf, certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM, err := selfSignedCertificatePEM(privateKey, now)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("renew automatic TLS certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // fixed automatic path selected from server DataDir
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("read automatic TLS key for renewal: %w", err)
	}
	renewed, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("validate renewed automatic TLS pair: %w", err)
	}
	if err := publish(certPath, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	slog.Info("renewed automatic TLS certificate", "cert", certPath, "key", keyPath, "identity_preserved", true)
	return renewed, nil
}

func validateAutomaticCertificate(
	cert tls.Certificate,
	leaf *x509.Certificate,
	certPath, keyPath string,
) (*ecdsa.PrivateKey, error) {
	if !bytes.Equal(leaf.RawSubject, leaf.RawIssuer) || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return nil, fmt.Errorf("automatic TLS certificate %q is not self-signed; configure managed certificates explicitly with -cert and -key", certPath)
	}
	privateKey, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("automatic TLS key %q is not an ECDSA P-256 key", keyPath)
	}
	return privateKey, nil
}

func replaceTLSCertificate(certPath string, certPEM []byte) error {
	temp, err := prepareTLSFile(certPath, certPEM, 0o644)
	if err != nil {
		return fmt.Errorf("prepare renewed TLS certificate: %w", err)
	}
	defer func() { _ = os.Remove(temp) }()
	if err := replaceTLSFile(temp, certPath); err != nil {
		return fmt.Errorf("publish renewed TLS certificate: %w", err)
	}
	return nil
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
