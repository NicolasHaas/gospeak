package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestTLSConfigAcceptsSystemPKI(t *testing.T) {
	root, rootKey := newTestCertificate(t, nil, nil, true, "root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	leaf, _ := newTestCertificate(t, root, rootKey, false, "server.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	roots := x509.NewCertPool()
	roots.AddCert(root)

	cfg, err := newTLSConfig("server.test:9600", "", roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.VerifyConnection(connectionState(leaf, root)); err != nil {
		t.Fatalf("VerifyConnection() = %v, want valid PKI certificate accepted", err)
	}
}

func TestTLSConfigRequiresTrustForSelfSignedCertificate(t *testing.T) {
	cert, _ := newTestCertificate(t, nil, nil, true, "private.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	cfg, err := newTLSConfig("private.test:9600", "", x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}

	err = cfg.VerifyConnection(connectionState(cert))
	var untrusted *UntrustedServerError
	if !errors.As(err, &untrusted) {
		t.Fatalf("VerifyConnection() error = %v, want UntrustedServerError", err)
	}
	if untrusted.Fingerprint != SPKIFingerprint(cert) {
		t.Fatalf("fingerprint = %q, want %q", untrusted.Fingerprint, SPKIFingerprint(cert))
	}
}

func TestTLSConfigAcceptsPinnedSelfSignedCertificate(t *testing.T) {
	cert, _ := newTestCertificate(t, nil, nil, true, "private.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	cfg, err := newTLSConfig("private.test:9600", SPKIFingerprint(cert), x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.VerifyConnection(connectionState(cert)); err != nil {
		t.Fatalf("VerifyConnection() = %v, want pinned certificate accepted", err)
	}
}

func TestTLSConfigRejectsChangedPin(t *testing.T) {
	oldCert, _ := newTestCertificate(t, nil, nil, true, "private.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	newCert, _ := newTestCertificate(t, nil, nil, true, "private.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	cfg, err := newTLSConfig("private.test:9600", SPKIFingerprint(oldCert), x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}

	err = cfg.VerifyConnection(connectionState(newCert))
	var changed *ServerIdentityChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("VerifyConnection() error = %v, want ServerIdentityChangedError", err)
	}
	if changed.Expected != SPKIFingerprint(oldCert) || changed.Received != SPKIFingerprint(newCert) {
		t.Fatalf("changed identity = %#v", changed)
	}
}

func TestTLSConfigRejectsExpiredPinnedCertificate(t *testing.T) {
	cert, _ := newTestCertificate(t, nil, nil, true, "private.test", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	cfg, err := newTLSConfig("private.test:9600", SPKIFingerprint(cert), x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.VerifyConnection(connectionState(cert)); err == nil {
		t.Fatal("VerifyConnection() = nil, want expired certificate rejected")
	}
}

func connectionState(certs ...*x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: certs}
}

func newTestCertificate(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, dnsName string, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	if parent == nil {
		parent = tmpl
		parentKey = key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}
