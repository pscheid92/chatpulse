package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

type Service interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// NoopService passes tokens through without encryption (dev/test mode).
type NoopService struct{}

func (NoopService) Encrypt(plaintext string) (string, error)  { return plaintext, nil }
func (NoopService) Decrypt(ciphertext string) (string, error) { return ciphertext, nil }

type AesGcmCryptoService struct {
	gcm cipher.AEAD
}

func NewAesGcmCryptoService(hexKey string) (*AesGcmCryptoService, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid encryption key hex: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	service := AesGcmCryptoService{gcm: gcm}
	return &service, nil
}

func (c *AesGcmCryptoService) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Seal appends the encrypted data to nonce, returning nonce || ciphertext || tag
	ciphertext := c.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

func (c *AesGcmCryptoService) Decrypt(ciphertext string) (string, error) {
	buffer, err := hex.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex: %w", err)
	}

	nonceSize := c.gcm.NonceSize()
	if len(buffer) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, cipherBytes := buffer[:nonceSize], buffer[nonceSize:]
	plainBytes, err := c.gcm.Open(nil, nonce, cipherBytes, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plainBytes), nil
}
