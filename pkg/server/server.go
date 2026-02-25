// Package server implements the GoSpeak server.
package server

import (
	"context"
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

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

// Config holds server configuration.
type Config struct {
	ControlAddr  string // TCP/TLS bind address (e.g. ":9600")
	VoiceAddr    string // UDP bind address (e.g. ":9601")
	DBPath       string // SQLite database path
	CertFile     string // TLS certificate file path
	KeyFile      string // TLS private key file path
	DataDir      string // directory for generated certs and data
	AllowNoToken bool   // allow users to join without a token (open server)
	ChannelsFile string // YAML file defining channels to create on startup
	MetricsAddr  string // HTTP bind address for /metrics endpoint (empty = disabled)

	// CLI-only actions (run and exit)
	ExportUsers    bool // export all users as YAML and exit
	ExportChannels bool // export all channels as YAML and exit
}

// Dependencies holds external dependencies for the server.
// Server assumes ownership of Store and will Close() it on shutdown.
type Dependencies struct {
	Store datastore.DataProviderFactory
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ControlAddr: ":9600",
		VoiceAddr:   ":9601",
		MetricsAddr: ":9602",
		DBPath:      "gospeak.db",
		DataDir:     ".",
	}
}

// loadOrGenerateTLS loads TLS cert/key from disk or generates a self-signed pair.
func loadOrGenerateTLS(cfg Config) (tls.Certificate, error) {
	certPath := cfg.CertFile
	keyPath := cfg.KeyFile

	if certPath == "" {
		certPath = filepath.Join(cfg.DataDir, "server.crt")
	}
	if keyPath == "" {
		keyPath = filepath.Join(cfg.DataDir, "server.key")
	}

	// Try loading existing cert
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		slog.Info("loaded TLS certificate", "cert", certPath)
		return cert, nil
	}

	// Generate self-signed certificate
	slog.Info("generating self-signed TLS certificate")
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{Organization: []string{"GoSpeak Server"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create cert: %w", err)
	}

	// Write cert
	certOut, err := os.Create(certPath) //nolint:gosec // path from server config
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		_ = certOut.Close()
		return tls.Certificate{}, fmt.Errorf("encode cert: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return tls.Certificate{}, fmt.Errorf("close cert file: %w", err)
	}

	// Write key
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600) //nolint:gosec // path from server config
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("write key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		_ = keyOut.Close()
		return tls.Certificate{}, fmt.Errorf("encode key: %w", err)
	}
	if err := keyOut.Close(); err != nil {
		return tls.Certificate{}, fmt.Errorf("close key file: %w", err)
	}

	slog.Info("TLS certificate generated", "cert", certPath, "key", keyPath)

	return tls.LoadX509KeyPair(certPath, keyPath)
}

// Server is the main GoSpeak server.
type Server struct {
	cfg         Config
	sessions    *SessionManager
	channels    *ChannelManager
	metrics     *Metrics
	store       datastore.DataProviderFactory
	controlConn net.Listener
	voiceConn   *net.UDPConn
	voiceKey    []byte // shared AES-128 key for all voice encryption
	ctx         context.Context
	cancel      context.CancelFunc
}

// New creates a new Server instance.
func New(cfg Config, deps Dependencies) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:      cfg,
		sessions: NewSessionManager(),
		channels: NewChannelManager(),
		metrics:  NewMetrics(),
		store:    deps.Store,
		ctx:      ctx,
		cancel:   cancel,
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
