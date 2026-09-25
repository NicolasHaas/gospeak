package crypto

import (
	"bytes"
	"testing"
)

func TestMediaCipherSuites(t *testing.T) {
	for _, suite := range []string{"aes128", "aes256", "chacha20"} {
		t.Run(suite, func(t *testing.T) {
			key, err := GenerateMediaKey(suite)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := NewMediaCipher(suite, key)
			if err != nil {
				t.Fatal(err)
			}
			header := []byte("authenticated header")
			sealed := cipher.Encrypt(9, 1, header, []byte("media"))
			plain, err := cipher.Decrypt(9, 1, header, sealed)
			if err != nil || !bytes.Equal(plain, []byte("media")) {
				t.Fatalf("round trip: %q %v", plain, err)
			}
			if _, err := cipher.Decrypt(9, 1, []byte("wrong header"), sealed); err == nil {
				t.Fatal("header tampering accepted")
			}
			sealed[0] ^= 1
			if _, err := cipher.Decrypt(9, 1, header, sealed); err == nil {
				t.Fatal("ciphertext tampering accepted")
			}
		})
	}
	for _, tc := range []struct {
		suite string
		key   []byte
	}{
		{"", make([]byte, 16)}, {"unknown", make([]byte, 16)},
		{"aes128", make([]byte, 32)}, {"aes256", make([]byte, 16)}, {"chacha20", make([]byte, 16)},
	} {
		if _, err := NewMediaCipher(tc.suite, tc.key); err == nil {
			t.Errorf("accepted suite %q with %d-byte key", tc.suite, len(tc.key))
		}
	}
}
