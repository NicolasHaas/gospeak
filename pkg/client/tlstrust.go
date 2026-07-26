package client

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
)

// UntrustedServerError reports a certificate that is not trusted by the
// platform PKI and has no saved TOFU pin.
type UntrustedServerError struct {
	Addr        string
	Fingerprint string
}

func (e *UntrustedServerError) Error() string {
	return fmt.Sprintf("server %s is not trusted; certificate fingerprint %s", e.Addr, e.Fingerprint)
}

// ServerIdentityChangedError reports that a server no longer presents its
// saved TOFU identity. Callers must require an explicit re-trust action.
type ServerIdentityChangedError struct {
	Addr     string
	Expected string
	Received string
}

func (e *ServerIdentityChangedError) Error() string {
	return fmt.Sprintf("server identity changed for %s (expected %s, received %s)", e.Addr, e.Expected, e.Received)
}

// SPKIFingerprint returns a stable SHA-256 fingerprint of a certificate's
// SubjectPublicKeyInfo. Pinning the public key tolerates certificate renewal
// with the same key while detecting an unexpected server identity.
func SPKIFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

func newTLSConfig(addr, expectedPin string, roots *x509.CertPool) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("client: invalid TLS address %q: %w", addr, err)
	}
	host = strings.Trim(host, "[]")

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         host,
		RootCAs:            roots,
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection below enforces PKI or an exact TOFU pin.
	}
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return fmt.Errorf("client: server %s provided no certificate", addr)
		}
		leaf := state.PeerCertificates[0]
		fingerprint := SPKIFingerprint(leaf)

		if expectedPin != "" {
			if fingerprint != expectedPin {
				return &ServerIdentityChangedError{Addr: addr, Expected: expectedPin, Received: fingerprint}
			}
			pinnedRoots := x509.NewCertPool()
			pinnedRoots.AddCert(leaf)
			if _, err := leaf.Verify(x509.VerifyOptions{
				Roots:     pinnedRoots,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				return fmt.Errorf("client: pinned certificate for %s is invalid: %w", addr, err)
			}
			return nil
		}

		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, verifyErr := leaf.Verify(x509.VerifyOptions{
			DNSName:       host,
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if verifyErr == nil {
			return nil
		}
		return &UntrustedServerError{Addr: addr, Fingerprint: fingerprint}
	}
	return cfg, nil
}

func tlsConfig(addr, expectedPin string) (*tls.Config, error) {
	return newTLSConfig(addr, expectedPin, nil)
}
