package crypto

import (
	"fmt"

	"golang.org/x/crypto/chacha20"
)

// DecryptChaCha20 decrypts data using ChaCha20 with the given key and nonce.
// Key must be 32 bytes, nonce must be 12 bytes (or 24 bytes for XChaCha20).
func DecryptChaCha20(data, key, nonce []byte) ([]byte, error) {
	if len(key) != chacha20.KeySize {
		return nil, fmt.Errorf("chacha20: invalid key size %d, want %d", len(key), chacha20.KeySize)
	}
	if len(nonce) != chacha20.NonceSize && len(nonce) != chacha20.NonceSizeX {
		return nil, fmt.Errorf("chacha20: invalid nonce size %d, want %d or %d", len(nonce), chacha20.NonceSize, chacha20.NonceSizeX)
	}

	cipher, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		return nil, fmt.Errorf("chacha20.NewUnauthenticatedCipher: %w", err)
	}

	decrypted := make([]byte, len(data))
	cipher.XORKeyStream(decrypted, data)
	return decrypted, nil
}
