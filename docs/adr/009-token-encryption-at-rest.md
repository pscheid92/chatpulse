# ADR-009: AES-256-GCM token encryption at rest

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse stores Twitch OAuth tokens in PostgreSQL. These tokens grant access to streamers' Twitch accounts and must be protected. Security concerns:

- **Database dumps** may leak to untrusted parties (backups sent to cloud storage, exported for analytics, logged during debugging)
- **Database compromises** could expose all tokens at once (SQL injection, stolen credentials, insider threat)
- **Compliance requirements** often mandate encryption of sensitive credentials at rest
- **Defense-in-depth principle:** Even if the database is compromised, tokens should remain protected

Standard practice: encrypt sensitive data before storing in the database (application-level encryption).

**Token types stored:**
- **Access tokens:** Short-lived (4 hours), grant API access
- **Refresh tokens:** Long-lived (months), used to get new access tokens

Both must be encrypted - a leaked refresh token allows permanent account access.

## Decision

**Encrypt OAuth tokens with AES-256-GCM before storing in PostgreSQL. Use a `crypto.Service` interface with two implementations: production encryption and dev/test plaintext passthrough.**

Specifically:

### Encryption Algorithm
- **AES-256-GCM** (Galois/Counter Mode) - authenticated encryption with 256-bit keys
- **12-byte random nonce** generated per encryption (never reused)
- **Tag included** in ciphertext (GCM provides authentication)
- **No padding** required (GCM is a stream cipher mode)

### Key Management
- **Encryption key** from `TOKEN_ENCRYPTION_KEY` environment variable
- **Format:** 64 hexadecimal characters (32 bytes) - e.g., `a1b2c3...` (output of `openssl rand -hex 32`)
- **Validation at startup:** Key must be exactly 64 hex chars or empty (empty = dev mode, no encryption)
- **Storage:** In production, load from secrets manager (AWS Secrets Manager, HashiCorp Vault, etc.) into environment variable

### Storage Format
- **Nonce prepended to ciphertext:** `[12-byte nonce][N-byte ciphertext+tag]`
- **Hex encoding:** Binary data → hex string for PostgreSQL TEXT column
- **Format:** `24 hex chars (nonce) + 2N hex chars (ciphertext)` where N = plaintext length + 16-byte tag

**Example:**
```
Plaintext token: "abc123def456..." (40 chars)
Nonce: 12 random bytes → 24 hex chars
Ciphertext: 40-byte token + 16-byte tag = 56 bytes → 112 hex chars
Stored value: "a1b2c3d4e5f6789012345678" + "def456..." (136 hex chars total)
                ↑ 24-char nonce           ↑ 112-char ciphertext+tag
```

### Interface Design

```go
// crypto/crypto.go
type Service interface {
    Encrypt(plaintext string) (string, error)  // Returns hex-encoded ciphertext
    Decrypt(ciphertext string) (string, error) // Takes hex-encoded input
}

// Production implementation
type AesGcmCryptoService struct {
    cipher cipher.AEAD  // AES-256-GCM cipher
}

// Dev/test implementation
type NoopService struct {}
func (s *NoopService) Encrypt(plaintext string) (string, error) { return plaintext, nil }
func (s *NoopService) Decrypt(ciphertext string) (string, error) { return ciphertext, nil }
```

### Integration
- **`UserRepo` takes `crypto.Service` via constructor** (injected from main.go)
- **Encrypt on write:** `Upsert(user)` and `UpdateTokens()` encrypt access/refresh tokens before `INSERT`/`UPDATE`
- **Decrypt on read:** `GetByID()` and `GetByOverlayUUID()` decrypt tokens after `SELECT`
- **No encryption in Redis:** Redis stores session state (sentiment values, ref counts), not tokens. Tokens only in PostgreSQL.

### Configuration
- **Production:** Set `TOKEN_ENCRYPTION_KEY=<64 hex chars>` → `AesGcmCryptoService`
- **Dev/test:** Omit `TOKEN_ENCRYPTION_KEY` or set to empty → `NoopService` (plaintext)

## Alternatives Considered

### 1. Database-level encryption (PostgreSQL TDE or pgcrypto)

**Description:** Use PostgreSQL's built-in encryption features:
- **Transparent Data Encryption (TDE):** Encrypts entire database at rest (Enterprise feature)
- **pgcrypto extension:** Provides SQL functions like `pgp_sym_encrypt()` / `pgp_sym_decrypt()`

**Rejected because:**
- **Requires PostgreSQL Enterprise or extensions:** TDE is not available in standard PostgreSQL. pgcrypto is an extension (must be installed, adds dependency).
- **Encrypts entire database or nothing:** TDE encrypts all tables. We only need to encrypt tokens (users, config don't need encryption). Overkill.
- **Key management is the same:** Whether we encrypt in Go or SQL, we still need to manage the encryption key (environment variable, secrets manager). No key management simplification.
- **Less portable:** pgcrypto syntax is PostgreSQL-specific. Application-level encryption works with any database (easier to migrate if needed).
- **Harder to test:** Mocking pgcrypto in tests is more complex than mocking `crypto.Service`.

**When this would make sense:**
- If we needed to encrypt many tables (not just tokens)
- If we were already using PostgreSQL Enterprise features

### 2. No encryption (store tokens in plaintext)

**Description:** Store access and refresh tokens in plaintext TEXT columns. Rely on database access control and network security.

**Rejected because:**
- **Fails security audits:** Most security standards (SOC 2, PCI DSS, GDPR) require encryption of sensitive credentials at rest. Plaintext tokens would fail audit.
- **Database dumps expose all tokens:** Backups, analytics exports, debug logs - all contain plaintext tokens. If any dump leaks, all tokens are compromised.
- **No defense-in-depth:** If the database is compromised (SQL injection, stolen credentials), the attacker gets all tokens immediately. Encryption adds a layer of defense (attacker needs database + encryption key).

**Accepted only for dev/test:** The `NoopService` implementation allows plaintext storage in local development (when `TOKEN_ENCRYPTION_KEY` is not set). This is acceptable because dev databases don't contain real user data.

### 3. External KMS (AWS KMS, HashiCorp Vault)

**Description:** Instead of managing the encryption key ourselves, use an external Key Management Service:
- **Encrypt:** Call KMS API to encrypt token (KMS does the encryption, we store the result)
- **Decrypt:** Call KMS API to decrypt token

**Rejected because:**
- **Adds external dependency:** Requires AWS account + KMS setup, or Vault deployment + management. Increases system complexity.
- **Network latency on every token access:** Every `GetByID()` call requires a network roundtrip to KMS (encrypt/decrypt operation). Adds 10-50ms latency.
- **Overkill for current scale:** We have one encryption key (not hundreds). Managing it as an environment variable is sufficient.
- **Can be added later:** The `crypto.Service` interface allows swapping implementations. We can switch to KMS later without changing `UserRepo` code.

**When this would make sense:**
- If we need automatic key rotation (KMS can rotate keys without re-encrypting data)
- If we need audit logs for every decrypt operation (compliance requirement)
- If we store many different encryption keys (one per tenant, etc.)

### 4. Per-user encryption keys (each user has unique key)

**Description:** Generate a unique encryption key for each user. Store key in a separate table or derive from user's password.

**Rejected because:**
- **Key management nightmare:** Thousands of keys to manage (one per user). If any key is lost, that user's tokens are unrecoverable.
- **No security improvement:** If the database is compromised, the attacker gets both the encrypted tokens AND the per-user keys (stored in same database). Same as single key.
- **Derive from password doesn't work:** Users log in via Twitch OAuth (no password). Can't derive key from password.

**When this would make sense:**
- If users provided passwords (not OAuth)
- If we needed per-user key revocation (compromise one user → rotate only their key)

## Consequences

### ✅ Positive

- **Defense-in-depth:** Database dump doesn't expose tokens (attacker needs dump + encryption key). Adds layer of protection.
- **Application-level encryption:** No PostgreSQL extensions required. Works with standard PostgreSQL, cloud-managed databases, or any SQL database.
- **Pluggable via interface:** Can swap `crypto.Service` implementation (switch to KMS, rotate algorithms) without changing `UserRepo`.
- **NoopService for dev:** Local development doesn't require key management. Set `TOKEN_ENCRYPTION_KEY` only in production.
- **Authenticated encryption (GCM):** Tampering with ciphertext is detectable. If someone modifies the database value directly, decryption fails (integrity check).

### ❌ Negative

- **Key rotation is complex:** If the encryption key is compromised, we need to:
  1. Generate new key
  2. Decrypt all tokens with old key
  3. Re-encrypt all tokens with new key
  4. Update `TOKEN_ENCRYPTION_KEY` environment variable

  This requires a migration script and downtime (or blue-green deployment).

- **Key must be securely stored:** The encryption key is as sensitive as the tokens themselves. Must store in secrets manager (AWS Secrets Manager, HashiCorp Vault) or secure key-value store, not in plain environment variables on disk.

- **Encryption/decryption overhead:** Every token read/write has ~100-500 microseconds overhead (AES-256-GCM is fast, but not zero-cost). Negligible compared to database query latency (5-20ms), but measurable.

- **Lost key = lost tokens:** If the encryption key is deleted or changed without migration, all tokens become unrecoverable. Users must re-authenticate via Twitch OAuth. Mitigation: key backup + documented recovery procedure.

### 🔄 Trade-offs

- **Chose defense-in-depth over simplicity:** We could skip encryption and rely only on database access control. We accept key management complexity for better security posture.
- **Accept key rotation complexity for security:** Rotating encryption keys is hard (need to re-encrypt all tokens). We accept this complexity because the alternative (no encryption) is worse for security audits.
- **Prioritize token protection over zero-config dev setup:** Production requires `TOKEN_ENCRYPTION_KEY` setup (not zero-config). We provide `NoopService` fallback for dev, but production must configure the key.

## Key Management Best Practices

### Generating the key
```bash
# Generate 32-byte (256-bit) random key, hex-encoded
openssl rand -hex 32
# Output: a1b2c3d4e5f6789012345678901234567890123456789012345678901234

# Save to secrets manager (example: AWS)
aws secretsmanager create-secret \
    --name chatpulse/token-encryption-key \
    --secret-string "a1b2c3d4e5f6789012345678901234567890123456789012345678901234"
```

### Loading in production
```bash
# Fetch from secrets manager at container startup
export TOKEN_ENCRYPTION_KEY=$(aws secretsmanager get-secret-value \
    --secret-id chatpulse/token-encryption-key \
    --query SecretString --output text)

# Run application (reads from env)
./server
```

### Key rotation procedure
1. **Generate new key:** `openssl rand -hex 32`
2. **Run migration script:**
   ```go
   // Pseudocode
   for user in allUsers {
       oldAccessToken = cryptoOld.Decrypt(user.AccessToken)
       oldRefreshToken = cryptoOld.Decrypt(user.RefreshToken)

       newAccessToken = cryptoNew.Encrypt(oldAccessToken)
       newRefreshToken = cryptoNew.Encrypt(oldRefreshToken)

       db.Update(user.ID, newAccessToken, newRefreshToken)
   }
   ```
3. **Deploy with new key:** Update `TOKEN_ENCRYPTION_KEY` environment variable
4. **Backup old key:** Keep old key in secrets manager for 30 days (in case of rollback)

## Related Decisions

- **ADR-007: Database vs Redis data separation** - Context: tokens stored in PostgreSQL (durable), not Redis (ephemeral). Encryption applies to PostgreSQL only.
- **ADR-008: UUID-based overlay access** - Contrast: overlay UUIDs are intentionally public (bearer tokens), not encrypted. Only OAuth tokens are encrypted.

## Implementation References

- `internal/crypto/crypto.go` - `Service` interface, `AesGcmCryptoService`, `NoopService`
- `internal/crypto/crypto_test.go` - Unit tests for encryption/decryption, key validation
- `internal/database/user_repository.go` - `UserRepo` takes `crypto.Service`, calls `Encrypt`/`Decrypt`
- `internal/config/config.go` - Validates `TOKEN_ENCRYPTION_KEY` format (64 hex chars or empty)
- `cmd/server/main.go` - Injects `crypto.Service` into `UserRepo` based on env var

## Observability

Monitor the following to detect encryption issues:

- **Decryption errors:** Count of `Decrypt()` failures. Should be 0 in normal operation. If >0, investigate (corrupted database, wrong key, key rotation issue).
- **Key validation failures:** Count of startup failures due to invalid `TOKEN_ENCRYPTION_KEY`. Should be 0 in production. If >0, fix configuration.
- **Encryption overhead:** P99 latency for `Encrypt()` and `Decrypt()`. Should be <1ms. If >10ms, investigate (CPU contention, memory pressure).

## Future Considerations

If key management becomes complex (multiple environments, key rotation frequency increases), we could:

1. **Migrate to AWS KMS:** Swap `AesGcmCryptoService` for `KmsCryptoService` (implements same `crypto.Service` interface). KMS handles key rotation automatically.
2. **Envelope encryption:** Encrypt tokens with a data encryption key (DEK), encrypt DEK with KMS. This allows key rotation without re-encrypting all tokens.
3. **Per-environment keys:** Different `TOKEN_ENCRYPTION_KEY` for dev/staging/prod. Prevents staging database dumps from decrypting prod tokens.

However, the current approach (single key, application-level AES-256-GCM) is sufficient for foreseeable needs.
