// Package crypto provides encryption services for data at rest.
//
// Implements AES-256-GCM encryption for OAuth tokens stored in PostgreSQL.
// Two implementations: AesGcmCryptoService (production) and NoopService (dev/test plaintext passthrough).
package crypto
