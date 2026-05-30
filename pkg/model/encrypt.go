package model

// EncryptMethod identifies the encryption algorithm used on segments.
type EncryptMethod int

const (
	EncryptMethodNone         EncryptMethod = iota
	EncryptMethodAES128                     // HLS AES-128-CBC
	EncryptMethodAES128ECB                  // HLS AES-128-ECB
	EncryptMethodSampleAES                  // HLS Sample-AES
	EncryptMethodSampleAESCTR               // HLS Sample-AES-CTR
	EncryptMethodCENC                        // MPEG-CENC
	EncryptMethodChaCha20                   // ChaCha20-Poly1305
)

// EncryptInfo holds encryption parameters for a segment or playlist.
type EncryptInfo struct {
	Method    EncryptMethod
	Key       []byte
	IV        []byte
	KeyID     []byte
	KeyURL    string
	KeyFormat string
}
