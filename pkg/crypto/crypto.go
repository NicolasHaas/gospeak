// Package crypto provides voice packet encryption and key management.
package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")
	ErrDecryptionFailed  = errors.New("crypto: decryption failed")
)

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

// VoiceCipher handles encryption for voice packets.
type VoiceCipher struct {
	aead      cipher.AEAD
	nonceSize int
}

// NewVoiceCipher creates a new voice cipher from given key.
func NewVoiceCipher(encryption pb.EncryptionInfo) (*VoiceCipher, error) {
	aead, err := NewCipher(encryption.EncryptionMethod, encryption.Key)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if nonceSize < 8 {
		return nil, fmt.Errorf("crypto: nonce size too small: expected >=%d, got %d", 8, nonceSize)
	}
	return &VoiceCipher{aead: aead, nonceSize: nonceSize}, nil
}

// buildNonce constructs a N byte nonce from sessionID and seqNum.
// Format: [sessionID(4) | seqNum(4) | zeros(N-8)]
// uint32 seqNum gives ~994 days at 50 pkt/s before wrap (vs ~21 min with uint16).
func buildNonce(sessionID uint32, seqNum uint32, nonceSize int) []byte {
	nonce := make([]byte, nonceSize)
	binary.BigEndian.PutUint32(nonce[0:4], sessionID)
	binary.BigEndian.PutUint32(nonce[4:8], seqNum)
	return nonce
}

// Encrypt encrypts an Opus frame, authenticating the header as additional data.
// Returns ciphertext with appended auth tag.
func (vc *VoiceCipher) Encrypt(sessionID uint32, seqNum uint32, header, opus []byte) []byte {
	nonce := buildNonce(sessionID, seqNum, vc.nonceSize)
	return vc.aead.Seal(nil, nonce, opus, header)
}

// Decrypt decrypts an encrypted Opus frame, verifying the header as additional data.
func (vc *VoiceCipher) Decrypt(sessionID uint32, seqNum uint32, header, ciphertext []byte) ([]byte, error) {
	nonce := buildNonce(sessionID, seqNum, vc.nonceSize)
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
