package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 64 hex chars = 32 bytes = valid AES-256 key
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNewAesGcmCryptoService_ValidKey(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewAesGcmCryptoService_InvalidHex(t *testing.T) {
	svc, err := NewAesGcmCryptoService("zzzz")
	assert.Error(t, err)
	assert.Nil(t, svc)
}

func TestNewAesGcmCryptoService_WrongKeyLength(t *testing.T) {
	tests := []struct {
		name   string
		hexKey string
	}{
		{"too short (31 bytes)", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"},
		{"too long (33 bytes)", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := NewAesGcmCryptoService(tt.hexKey)
			assert.Error(t, err)
			assert.Nil(t, svc)
		})
	}
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)

	plaintext := "my-secret-token-12345"

	ciphertext, err := svc.Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)
	assert.Greater(t, len(ciphertext), len(plaintext))

	decrypted, err := svc.Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptDecrypt_UniqueNonces(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)

	// Encrypting the same plaintext twice should produce different ciphertexts
	ct1, err := svc.Encrypt("same-value")
	require.NoError(t, err)
	ct2, err := svc.Encrypt("same-value")
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2)
}

func TestDecrypt_InvalidHex(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)

	_, err = svc.Decrypt("not-valid-hex!!!")
	assert.Error(t, err)
}

func TestDecrypt_TooShort(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)

	_, err = svc.Decrypt("abcd")
	assert.Error(t, err)
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	svc, err := NewAesGcmCryptoService(testKey)
	require.NoError(t, err)

	ciphertext, err := svc.Encrypt("secret")
	require.NoError(t, err)

	// Flip a byte in the ciphertext (after the nonce)
	tampered := []byte(ciphertext)
	tampered[len(tampered)-1] ^= 0xff
	_, err = svc.Decrypt(string(tampered))
	assert.Error(t, err)
}

func TestNoopService_Passthrough(t *testing.T) {
	svc := NoopService{}

	ciphertext, err := svc.Encrypt("plaintext")
	require.NoError(t, err)
	assert.Equal(t, "plaintext", ciphertext)

	decrypted, err := svc.Decrypt("plaintext")
	require.NoError(t, err)
	assert.Equal(t, "plaintext", decrypted)
}
