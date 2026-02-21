package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"golang.org/x/crypto/chacha20poly1305"
)

type EncryptionKeySize uint32

const (
	AES128KeySize   EncryptionKeySize = 16
	AES256KeySize   EncryptionKeySize = 32
	Chacha20KeySize EncryptionKeySize = chacha20poly1305.KeySize
)

func NewCipher(method pb.EncryptionMethod, key []byte) (cipher.AEAD, error) {

	var aead cipher.AEAD
	var err error
	keylength := len(key)
	switch method {
	case pb.AES128:
		if keylength != int(AES128KeySize) {
			return nil, fmt.Errorf("crypto: invalid aes128 key length: expected %d, got %d", int(AES128KeySize), keylength)
		}
		aead, err = newAESGCMCipher(key)
	case pb.AES256:
		if keylength != int(AES256KeySize) {
			return nil, fmt.Errorf("crypto: invalid aes256 key length: expected %d, got %d", int(AES256KeySize), keylength)
		}
		aead, err = newAESGCMCipher(key)
	case pb.CHACHA20:
		if keylength != int(Chacha20KeySize) {
			return nil, fmt.Errorf("crypto: invalid chacha20poly1305 key length: expected %d, got %d", int(Chacha20KeySize), keylength)
		}
		aead, err = newChacha20Poly1305Cipher(key)
	default:
		err = fmt.Errorf("crypto: unknown encryption method: %v", method)
	}
	if err != nil {
		return nil, err
	}
	return aead, nil
}
func newAESGCMCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	return aead, nil
}

func newChacha20Poly1305Cipher(key []byte) (cipher.AEAD, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new chacha20 cipher: %w", err)
	}
	return aead, nil
}

// GenerateKey generates a random key of a given size.
func GenerateKey(keysize EncryptionKeySize) ([]byte, error) {
	key := make([]byte, keysize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("crypto: generate key: %w", err)
	}
	return key, nil
}
