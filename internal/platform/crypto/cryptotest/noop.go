package cryptotest

// NoopService passes tokens through without encryption. Test use only.
type NoopService struct{}

func (NoopService) Encrypt(plaintext string) (string, error)  { return plaintext, nil }
func (NoopService) Decrypt(ciphertext string) (string, error) { return ciphertext, nil }
