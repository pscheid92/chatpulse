# Secret Rotation Procedures

## Overview

ChatPulse supports zero-downtime rotation of cryptographic secrets through key versioning. This document describes rotation procedures for encryption keys, session secrets, and webhook secrets.

## Token Encryption Key Rotation

**Frequency:** Every 90 days or on compromise

**Prerequisites:**
- Access to environment configuration
- Ability to perform rolling restarts

**Procedure:**

### 1. Generate New Key

```bash
openssl rand -hex 32
```

### 2. Add to Configuration (Keep Old Key)

Update `.env` or environment variables:

```bash
# Before rotation (single key):
# TOKEN_ENCRYPTION_KEY=old64charhexkey...
# CURRENT_KEY_VERSION=v1

# During rotation (add v2, keep v1):
TOKEN_ENCRYPTION_KEYS='{"v1":"old64charhexkey...","v2":"new64charhexkey..."}'
CURRENT_KEY_VERSION=v2
```

### 3. Deploy with Rolling Restart

- New encryptions use v2
- Old tokens still readable with v1
- Background re-encryption job starts automatically

### 4. Monitor Re-Encryption Progress

Check Prometheus metrics:

```promql
# Legacy decrypts (should decrease over time)
key_rotation_legacy_decrypts_total{version="v1"}

# Re-encryption progress
rate(key_rotation_re_encryptions_total[5m])

# Re-encryption failures
rate(key_rotation_re_encryption_failures_total[5m])
```

**Default re-encryption rate:** 100 users/hour (configurable via `reEncryptBatchSize`)

### 5. Remove Old Key After Grace Period

Wait until `key_rotation_legacy_decrypts_total{version="v1"}` reaches zero (typically 7 days for active users):

```bash
TOKEN_ENCRYPTION_KEYS='{"v2":"new64charhexkey..."}'
CURRENT_KEY_VERSION=v2
```

Rolling restart to apply.

### Emergency Rotation (Key Compromise)

If the encryption key is compromised:

1. **Immediate:** Revoke all user tokens (force re-authentication):
   ```sql
   DELETE FROM users WHERE token_expiry IS NOT NULL;
   ```

2. **Follow standard rotation procedure** but skip the grace period - remove old key immediately after deployment.

3. **Notify users** to re-authenticate via Twitch OAuth.

## Session Secret Rotation

**Impact:** All users logged out

**Procedure:**

1. Generate new secret:
   ```bash
   openssl rand -base64 32
   ```

2. Update `SESSION_SECRET` environment variable

3. Rolling restart

4. All users must re-login

**Note:** Session secret rotation does NOT use versioning. It's an immediate cutover that invalidates all sessions.

## Webhook Secret Rotation

**Impact:** None (automatic Twitch API update)

**Procedure:**

1. Generate new secret (10-100 characters):
   ```bash
   openssl rand -base64 32 | head -c 50
   ```

2. Call admin endpoint:
   ```bash
   curl -X POST http://localhost:8080/admin/rotate-webhook-secret \
     -H "Content-Type: application/json" \
     -d '{"new_secret":"your_new_secret_here"}'
   ```

3. Restart app to persist new secret

**Note:** Admin endpoint updates Twitch conduit automatically. Environment variable change + restart required to persist.

## Architecture

### Key Versioning

Ciphertext format: `v2:hexciphertext`

- `v2`: Key version
- `:`: Separator
- `hexciphertext`: Nonce + ciphertext + tag (hex-encoded)

Legacy format (no version prefix) is decrypted using current key.

### Background Re-Encryption

The `app.Service` runs an hourly background job (`startReEncryptTimer`) that:

1. Queries `users` table for tokens not matching current version prefix
2. Re-encrypts up to 100 tokens per run (via `UserRepo.UpdateTokens`)
3. Tracks progress via Prometheus metrics

**Automatic activation:** Re-encryption starts automatically when `TOKEN_ENCRYPTION_KEYS` is configured.

### Metrics

**Key rotation metrics:**
- `key_rotation_legacy_decrypts_total{version}` — Decryptions using old keys (counter)
- `key_rotation_re_encryptions_total` — Successful re-encryptions (counter)
- `key_rotation_re_encryption_failures_total` — Failed re-encryption attempts (counter)

**Use case:** Monitor `legacy_decrypts_total{version="v1"}` to determine when old key can be safely removed.

## Configuration Reference

### Versioned Keys Mode (Recommended)

```bash
TOKEN_ENCRYPTION_KEYS='{"v1":"key1...","v2":"key2..."}'
CURRENT_KEY_VERSION=v2
```

- Multiple keys active simultaneously
- New encryptions use `CURRENT_KEY_VERSION`
- Old ciphertexts decrypt with legacy keys
- Background re-encryption enabled

### Single Key Mode (Legacy, No Rotation)

```bash
TOKEN_ENCRYPTION_KEY=64charhexkey...
```

- Single key only
- No rotation support
- Automatically converted to `{"v1":"..."}` internally

### No Encryption (Dev/Test)

Omit both `TOKEN_ENCRYPTION_KEY` and `TOKEN_ENCRYPTION_KEYS`:

- Tokens stored in plaintext
- NOT RECOMMENDED for production

## Troubleshooting

### Re-Encryption Stuck

**Symptom:** `key_rotation_legacy_decrypts_total` not decreasing

**Diagnosis:**
1. Check `key_rotation_re_encryption_failures_total` rate
2. Check logs for `"Failed to re-encrypt user tokens"` errors
3. Verify database connectivity

**Resolution:**
- Fix underlying issue (DB connection, permissions)
- Re-encryption automatically retries on next hourly run

### Unknown Key Version Error

**Symptom:** Users unable to log in, errors like `"unknown key version: v1"`

**Cause:** Old key removed before re-encryption complete

**Resolution:**
1. Immediately restore old key to `TOKEN_ENCRYPTION_KEYS`
2. Deploy to all instances
3. Wait for re-encryption to complete before removing again

### High Legacy Decrypt Rate

**Symptom:** `key_rotation_legacy_decrypts_total` increasing rapidly

**Cause:** Active users whose tokens haven't been re-encrypted yet

**Resolution:**
- This is expected during rotation
- Re-encryption happens gradually (100/hour)
- Users with frequently refreshed tokens re-encrypt faster
- Inactive users re-encrypt when they next log in

## Best Practices

1. **Test in staging first** — Verify rotation procedure with non-production keys
2. **Monitor metrics** — Set up alerting on `re_encryption_failures_total`
3. **Grace period** — Wait 7+ days before removing old key (accommodates inactive users)
4. **Emergency plan** — Document who can force-logout all users if key is compromised
5. **Audit trail** — Log all key rotation events for compliance

## Security Considerations

- **Key storage:** Store encryption keys in secrets management (Vault, AWS Secrets Manager)
- **Key transmission:** Never transmit keys over unencrypted channels
- **Key access:** Limit access to encryption keys (least privilege principle)
- **Compliance:** 90-day rotation meets SOC 2, PCI-DSS, HIPAA requirements
- **Compromise response:** Emergency rotation + force re-auth takes <5 minutes

## Future Enhancements

Potential improvements not yet implemented:

- [ ] Automatic key rotation schedule (cron-like)
- [ ] Key derivation from master secret (HKDF)
- [ ] Hardware security module (HSM) integration
- [ ] Audit log for all decrypt operations
- [ ] Per-user re-encryption priority (prioritize active users)
