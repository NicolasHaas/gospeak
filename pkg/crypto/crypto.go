// Package crypto provides voice packet encryption and key management.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")
	ErrDecryptionFailed  = errors.New("crypto: decryption failed")
)

// GenerateKey generates a random AES-128 key (16 bytes).
func GenerateKey() ([]byte, error) {
	key := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("crypto: generate key: %w", err)
	}
	return key, nil
}

// GenerateToken generates a random token string (32 bytes, hex-like).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("crypto: generate token: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}

// HashToken hashes a raw token string with SHA-256.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h[:])
}

// HashPassword hashes a password using Argon2id.
func HashPassword(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
}

// VoiceCipher handles AES-128-GCM encryption for voice packets.
type VoiceCipher struct {
	aead cipher.AEAD
}

// NewVoiceCipher creates a new voice cipher from a 16-byte AES key.
func NewVoiceCipher(key []byte) (*VoiceCipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	return &VoiceCipher{aead: aead}, nil
}

// buildNonce constructs a 12-byte nonce from sessionID and seqNum.
// Format: [sessionID(4) | seqNum(4) | zeros(4)]
// uint32 seqNum gives ~994 days at 50 pkt/s before wrap (vs ~21 min with uint16).
func buildNonce(sessionID uint32, seqNum uint32) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint32(nonce[0:4], sessionID)
	binary.BigEndian.PutUint32(nonce[4:8], seqNum)
	return nonce
}

// Encrypt encrypts an Opus frame, authenticating the header as additional data.
// Returns ciphertext with appended auth tag.
func (vc *VoiceCipher) Encrypt(sessionID uint32, seqNum uint32, header, opus []byte) []byte {
	nonce := buildNonce(sessionID, seqNum)
	return vc.aead.Seal(nil, nonce, opus, header)
}

// Decrypt decrypts an encrypted Opus frame, verifying the header as additional data.
func (vc *VoiceCipher) Decrypt(sessionID uint32, seqNum uint32, header, ciphertext []byte) ([]byte, error) {
	nonce := buildNonce(sessionID, seqNum)
	plaintext, err := vc.aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return plaintext, nil
}

// Overhead returns the number of bytes the AEAD adds to the plaintext (GCM auth tag).
func (vc *VoiceCipher) Overhead() int {
	return vc.aead.Overhead()
}
