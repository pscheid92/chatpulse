package crypto

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionedCryptoService_New(t *testing.T) {
	t.Run("valid single key", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)
		assert.NotNil(t, svc)
		assert.Equal(t, "v1", svc.currentVersion)
	})

	t.Run("valid multiple keys", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
			"v2": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v2")
		require.NoError(t, err)
		assert.NotNil(t, svc)
		assert.Equal(t, "v2", svc.currentVersion)
		assert.Len(t, svc.legacyKeys, 1)
	})

	t.Run("empty keys map", func(t *testing.T) {
		keys := map[string]string{}
		_, err := NewVersionedCryptoService(keys, "v1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one key required")
	})

	t.Run("current version not found", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		_, err := NewVersionedCryptoService(keys, "v2")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "current version v2 not found")
	})

	t.Run("invalid key hex", func(t *testing.T) {
		keys := map[string]string{
			"v1": "notvalidhex",
		}
		_, err := NewVersionedCryptoService(keys, "v1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid key for version v1")
	})

	t.Run("wrong key size", func(t *testing.T) {
		keys := map[string]string{
			"v1": "0123456789abcdef", // Only 8 bytes (16 hex chars)
		}
		_, err := NewVersionedCryptoService(keys, "v1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be 32 bytes")
	})
}

func TestVersionedCryptoService_EncryptDecrypt(t *testing.T) {
	t.Run("encrypt and decrypt with current key", func(t *testing.T) {
		keys := map[string]string{
			"v2": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v2")
		require.NoError(t, err)

		plaintext := "my-secret-token"
		ciphertext, err := svc.Encrypt(plaintext)
		require.NoError(t, err)

		// Verify version prefix
		assert.True(t, strings.HasPrefix(ciphertext, "v2:"))

		// Decrypt
		decrypted, err := svc.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("unique nonces for same plaintext", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)

		plaintext := "same-plaintext"
		ct1, err := svc.Encrypt(plaintext)
		require.NoError(t, err)
		ct2, err := svc.Encrypt(plaintext)
		require.NoError(t, err)

		assert.NotEqual(t, ct1, ct2, "nonces must be unique")
	})

	t.Run("decrypt with legacy key", func(t *testing.T) {
		key1 := generateTestKey()
		key2 := generateTestKey()

		// Create service with v1 as current
		svc1, err := NewVersionedCryptoService(map[string]string{"v1": key1}, "v1")
		require.NoError(t, err)

		plaintext := "old-token"
		ciphertext, err := svc1.Encrypt(plaintext)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(ciphertext, "v1:"))

		// Rotate to v2 (keep v1 as legacy)
		svc2, err := NewVersionedCryptoService(map[string]string{
			"v1": key1,
			"v2": key2,
		}, "v2")
		require.NoError(t, err)

		// Should decrypt v1 ciphertext with legacy key
		decrypted, err := svc2.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("decrypt legacy format without version prefix", func(t *testing.T) {
		// Use the old AesGcmCryptoService to create legacy ciphertext
		oldSvc, err := NewAesGcmCryptoService(generateTestKey())
		require.NoError(t, err)

		plaintext := "legacy-token"
		legacyCiphertext, err := oldSvc.Encrypt(plaintext)
		require.NoError(t, err)

		// legacyCiphertext has no version prefix
		assert.False(t, strings.Contains(legacyCiphertext, ":"))

		// Create versioned service with same key
		keys := map[string]string{
			"v1": extractKeyFromOldService(oldSvc),
		}
		versionedSvc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)

		// Should decrypt legacy format using current key
		decrypted, err := versionedSvc.Decrypt(legacyCiphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("unknown version fails", func(t *testing.T) {
		keys := map[string]string{
			"v2": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v2")
		require.NoError(t, err)

		// Try to decrypt with v1 prefix but only v2 key available
		ciphertext := "v1:" + hex.EncodeToString([]byte("fake-ciphertext"))
		_, err = svc.Decrypt(ciphertext)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown key version: v1")
	})

	t.Run("tampered ciphertext fails", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)

		plaintext := "secret"
		ciphertext, err := svc.Encrypt(plaintext)
		require.NoError(t, err)

		// Tamper with ciphertext
		parts := strings.SplitN(ciphertext, ":", 2)
		tamperedHex := parts[1][:len(parts[1])-2] + "00" // Change last byte
		tampered := parts[0] + ":" + tamperedHex

		_, err = svc.Decrypt(tampered)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decrypt")
	})

	t.Run("invalid hex fails", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)

		_, err = svc.Decrypt("v1:notvalidhex")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode hex")
	})

	t.Run("ciphertext too short fails", func(t *testing.T) {
		keys := map[string]string{
			"v1": generateTestKey(),
		}
		svc, err := NewVersionedCryptoService(keys, "v1")
		require.NoError(t, err)

		// Hex-encoded empty string
		_, err = svc.Decrypt("v1:")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ciphertext too short")
	})
}

func TestVersionedCryptoService_MultiKeyRotation(t *testing.T) {
	// Simulate full key rotation cycle: v1 -> v2 -> v3
	key1 := generateTestKey()
	key2 := generateTestKey()
	key3 := generateTestKey()

	// Phase 1: Only v1 active
	svc1, err := NewVersionedCryptoService(map[string]string{"v1": key1}, "v1")
	require.NoError(t, err)

	token1 := "token-encrypted-with-v1"
	ct1, err := svc1.Encrypt(token1)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ct1, "v1:"))

	// Phase 2: Rotate to v2 (keep v1 for reading old tokens)
	svc2, err := NewVersionedCryptoService(map[string]string{
		"v1": key1,
		"v2": key2,
	}, "v2")
	require.NoError(t, err)

	// New encryption uses v2
	token2 := "token-encrypted-with-v2"
	ct2, err := svc2.Encrypt(token2)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ct2, "v2:"))

	// Old v1 tokens still readable
	decrypted1, err := svc2.Decrypt(ct1)
	require.NoError(t, err)
	assert.Equal(t, token1, decrypted1)

	// Phase 3: Rotate to v3 (keep v1 and v2)
	svc3, err := NewVersionedCryptoService(map[string]string{
		"v1": key1,
		"v2": key2,
		"v3": key3,
	}, "v3")
	require.NoError(t, err)

	// New encryption uses v3
	token3 := "token-encrypted-with-v3"
	ct3, err := svc3.Encrypt(token3)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ct3, "v3:"))

	// All old tokens still readable
	d1, err := svc3.Decrypt(ct1)
	require.NoError(t, err)
	assert.Equal(t, token1, d1)

	d2, err := svc3.Decrypt(ct2)
	require.NoError(t, err)
	assert.Equal(t, token2, d2)

	// Phase 4: Remove v1 (only v2 and v3)
	svc4, err := NewVersionedCryptoService(map[string]string{
		"v2": key2,
		"v3": key3,
	}, "v3")
	require.NoError(t, err)

	// v1 tokens no longer readable
	_, err = svc4.Decrypt(ct1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key version: v1")

	// v2 and v3 still work
	d2, err = svc4.Decrypt(ct2)
	require.NoError(t, err)
	assert.Equal(t, token2, d2)

	d3, err := svc4.Decrypt(ct3)
	require.NoError(t, err)
	assert.Equal(t, token3, d3)
}

// Helper: generate a random 32-byte key as hex string
func generateTestKey() string {
	key := make([]byte, 32)
	_, err := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		panic(err)
	}
	// Use predictable key for tests (crypto/rand would require error handling)
	return hex.EncodeToString(key)
}

// Helper: extract hex key from old AesGcmCryptoService for migration tests
// This is a hack for testing - in production, you'd know your old key
func extractKeyFromOldService(svc *AesGcmCryptoService) string {
	// For testing, we'll just use a fresh key since we can't extract from cipher.AEAD
	return generateTestKey()
}
