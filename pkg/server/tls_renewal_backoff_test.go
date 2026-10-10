package server

import (
	"crypto/tls"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticTLSRenewalFailureBackoff(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := Config{DataDir: t.TempDir()}
	source, err := newTLSCertificateSourceAt(cfg, started)
	if err != nil {
		t.Fatal(err)
	}
	cached := source.cert
	failure := errors.New("injected publication failure")
	attempts := 0
	fail := true
	source.load = func(_ Config, now time.Time) (tls.Certificate, error) {
		attempts++
		return renewAutomaticTLSWithPublisher(filepath.Join(cfg.DataDir, generatedCertName), filepath.Join(cfg.DataDir, generatedKeyName), *cached, source.leaf, now, func(path string, data []byte) error {
			if fail {
				return failure
			}
			return replaceTLSCertificate(path, data)
		})
	}
	// Expiry inside the backoff must fail closed, without another disk write.
	renewAt := source.leaf.NotAfter.Add(-30 * time.Second)
	for _, offset := range []time.Duration{0, time.Second, 30 * time.Second} {
		got, err := source.getCertificateAt(renewAt.Add(offset))
		if err != nil || got != cached {
			t.Fatalf("valid cache at %s: certificate=%p err=%v", offset, got, err)
		}
	}
	if got, err := source.getCertificateAt(renewAt.Add(31 * time.Second)); got != nil || !errors.Is(err, failure) {
		t.Fatalf("expired cache: certificate=%p err=%v", got, err)
	}
	if attempts != 1 {
		t.Fatalf("publication attempts within backoff = %d, want 1", attempts)
	}
	fail = false
	got, err := source.getCertificateAt(renewAt.Add(time.Minute))
	if err != nil || got == cached || attempts != 2 {
		t.Fatalf("retry boundary: certificate=%p attempts=%d err=%v", got, attempts, err)
	}
	if string(mustParseLeaf(t, *got).RawSubjectPublicKeyInfo) != string(mustParseLeaf(t, *cached).RawSubjectPublicKeyInfo) {
		t.Fatal("renewal changed pinned identity")
	}
	if _, err := source.getCertificateAt(renewAt.Add(2 * time.Minute)); err != nil || attempts != 2 {
		t.Fatalf("successful renewal not cached: attempts=%d err=%v", attempts, err)
	}
}
