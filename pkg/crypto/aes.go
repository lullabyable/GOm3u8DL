package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// DecryptAES128CBC decrypts data using AES-128-CBC with PKCS7 padding.
func DecryptAES128CBC(data, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("data length %d is not a multiple of block size", len(data))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("IV length %d != block size %d", len(iv), aes.BlockSize)
	}

	decrypted := make([]byte, len(data))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, data)

	// Remove PKCS7 padding
	if len(decrypted) > 0 {
		padLen := int(decrypted[len(decrypted)-1])
		if padLen > 0 && padLen <= aes.BlockSize {
			// Verify padding
			valid := true
			for i := len(decrypted) - padLen; i < len(decrypted); i++ {
				if decrypted[i] != byte(padLen) {
					valid = false
					break
				}
			}
			if valid {
				decrypted = decrypted[:len(decrypted)-padLen]
			}
		}
	}

	return decrypted, nil
}

// DecryptAES128ECB decrypts data using AES-128-ECB.
// Note: ECB mode is insecure; only used for HLS compatibility.
func DecryptAES128ECB(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("data length %d is not a multiple of block size", len(data))
	}

	decrypted := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(decrypted[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}

	// Remove PKCS7 padding
	if len(decrypted) > 0 {
		padLen := int(decrypted[len(decrypted)-1])
		if padLen > 0 && padLen <= aes.BlockSize {
			valid := true
			for i := len(decrypted) - padLen; i < len(decrypted); i++ {
				if decrypted[i] != byte(padLen) {
					valid = false
					break
				}
			}
			if valid {
				decrypted = decrypted[:len(decrypted)-padLen]
			}
		}
	}

	return decrypted, nil
}
