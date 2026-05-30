package crypto

import (
	"testing"
)

func TestDecryptChaCha20(t *testing.T) {
	// ChaCha20 is a stream cipher, so encrypt == decrypt with same key/nonce.
	key := make([]byte, 32) // all zeros
	for i := range key {
		key[i] = byte(i)
	}
	nonce := make([]byte, 12) // all zeros

	plaintext := []byte("Hello, ChaCha20 encryption test!")

	// Encrypt
	encrypted, err := DecryptChaCha20(plaintext, key, nonce)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Decrypt (same operation for stream cipher)
	decrypted, err := DecryptChaCha20(encrypted, key, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip failed: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptChaCha20InvalidKey(t *testing.T) {
	data := []byte("test")
	_, err := DecryptChaCha20(data, []byte("short"), make([]byte, 12))
	if err == nil {
		t.Error("expected error for invalid key size")
	}
}

func TestDecryptChaCha20InvalidNonce(t *testing.T) {
	key := make([]byte, 32)
	_, err := DecryptChaCha20([]byte("test"), key, []byte("short"))
	if err == nil {
		t.Error("expected error for invalid nonce size")
	}
}

func TestDecryptChaCha20XChaCha20(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	nonce := make([]byte, 24) // XChaCha20 nonce

	plaintext := []byte("XChaCha20 test with 24-byte nonce!")

	encrypted, err := DecryptChaCha20(plaintext, key, nonce)
	if err != nil {
		t.Fatalf("XChaCha20 encrypt: %v", err)
	}

	decrypted, err := DecryptChaCha20(encrypted, key, nonce)
	if err != nil {
		t.Fatalf("XChaCha20 decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("XChaCha20 roundtrip failed: got %q, want %q", decrypted, plaintext)
	}
}
