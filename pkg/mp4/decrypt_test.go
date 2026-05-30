package mp4

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// --- ParseTenc tests ---

func TestParseTencV0(t *testing.T) {
	// version 0: version(1) + flags(3) + reserved(4) + isEncrypted(1) + IVSize(1) + reserved(1) + KID(16) = 27 bytes
	body := make([]byte, 27)
	body[0] = 0 // version 0
	body[8] = 1 // isEncrypted
	body[9] = 8 // IV size
	// KID at offset 11
	for i := 0; i < 16; i++ {
		body[11+i] = byte(i + 1)
	}

	tenc, err := ParseTenc(body)
	if err != nil {
		t.Fatalf("ParseTenc: %v", err)
	}
	if !tenc.IsEncrypted {
		t.Error("expected IsEncrypted=true")
	}
	if tenc.IVSize != 8 {
		t.Errorf("IVSize = %d, want 8", tenc.IVSize)
	}
	for i := 0; i < 16; i++ {
		if tenc.KID[i] != byte(i+1) {
			t.Errorf("KID[%d] = %d, want %d", i, tenc.KID[i], i+1)
		}
	}
}

func TestParseTencV1(t *testing.T) {
	// version 1: version(1) + flags(3) + isEncrypted(1) + IVSize(1) + KID(16) = 22 bytes
	body := make([]byte, 22)
	body[0] = 1 // version 1
	body[4] = 1 // isEncrypted
	body[5] = 8 // IV size
	for i := 0; i < 16; i++ {
		body[6+i] = byte(0xAA)
	}

	tenc, err := ParseTenc(body)
	if err != nil {
		t.Fatalf("ParseTenc: %v", err)
	}
	if !tenc.IsEncrypted {
		t.Error("expected IsEncrypted=true")
	}
	if tenc.IVSize != 8 {
		t.Errorf("IVSize = %d, want 8", tenc.IVSize)
	}
	if tenc.KID[0] != 0xAA {
		t.Errorf("KID[0] = 0x%02x, want 0xAA", tenc.KID[0])
	}
}

func TestParseTencNotEncrypted(t *testing.T) {
	body := make([]byte, 22)
	body[0] = 1
	body[4] = 0 // not encrypted

	tenc, err := ParseTenc(body)
	if err != nil {
		t.Fatalf("ParseTenc: %v", err)
	}
	if tenc.IsEncrypted {
		t.Error("expected IsEncrypted=false")
	}
}

func TestParseTencTooShort(t *testing.T) {
	_, err := ParseTenc([]byte{0, 0, 0, 0})
	if err == nil {
		t.Error("expected error for short body")
	}
}

// --- ParseSinf tests ---

func TestParseSinf(t *testing.T) {
	// Build schm box
	schmBody := make([]byte, 12)
	schmBody[0] = 0 // version
	copy(schmBody[4:8], []byte("cenc"))
	binary.BigEndian.PutUint32(schmBody[8:12], 0x00010000) // version 1.0

	schmBox := makeBox("schm", schmBody)

	// Build tenc inside schi
	tencBody := make([]byte, 22)
	tencBody[0] = 1
	tencBody[4] = 1 // encrypted
	tencBody[5] = 8 // IV size
	tencBox := makeBox("tenc", tencBody)

	schiBox := makeBox("schi", tencBox)

	// Build sinf
	sinfBody := make([]byte, len(schmBox)+len(schiBox))
	copy(sinfBody, schmBox)
	copy(sinfBody[len(schmBox):], schiBox)

	sinf, err := ParseSinf(sinfBody)
	if err != nil {
		t.Fatalf("ParseSinf: %v", err)
	}
	if sinf.SchemeType != "cenc" {
		t.Errorf("SchemeType = %q, want cenc", sinf.SchemeType)
	}
	if sinf.SchemeVersion != 0x00010000 {
		t.Errorf("SchemeVersion = 0x%08x, want 0x00010000", sinf.SchemeVersion)
	}
	if sinf.TrackEncryption == nil {
		t.Fatal("expected TrackEncryption to be non-nil")
	}
	if !sinf.TrackEncryption.IsEncrypted {
		t.Error("expected IsEncrypted=true")
	}
}

func TestParseSinfNoSchi(t *testing.T) {
	schmBody := make([]byte, 12)
	copy(schmBody[4:8], []byte("cenc"))
	schmBox := makeBox("schm", schmBody)

	sinf, err := ParseSinf(schmBox)
	if err != nil {
		t.Fatalf("ParseSinf: %v", err)
	}
	if sinf.SchemeType != "cenc" {
		t.Errorf("SchemeType = %q, want cenc", sinf.SchemeType)
	}
	if sinf.TrackEncryption != nil {
		t.Error("expected TrackEncryption to be nil")
	}
}

// --- ParsePssh tests ---

func TestParsePsshV0(t *testing.T) {
	// v0: version(1) + flags(3) + systemID(16) + dataLen(4) + data
	body := make([]byte, 24+8)
	body[0] = 0 // version
	for i := 0; i < 16; i++ {
		body[4+i] = byte(i) // system ID
	}
	binary.BigEndian.PutUint32(body[20:24], 8) // data length
	copy(body[24:], []byte{1, 2, 3, 4, 5, 6, 7, 8})

	pssh, err := ParsePssh(body)
	if err != nil {
		t.Fatalf("ParsePssh: %v", err)
	}
	if pssh.SystemID[0] != 0 || pssh.SystemID[15] != 15 {
		t.Errorf("SystemID mismatch")
	}
	if len(pssh.Data) != 8 {
		t.Errorf("Data len = %d, want 8", len(pssh.Data))
	}
}

func TestParsePsshV1(t *testing.T) {
	// v1: version(1) + flags(3) + systemID(16) + kidCount(4) + KIDs + dataLen(4) + data
	kidCount := 2
	bodyLen := 24 + kidCount*16 + 4 + 4
	body := make([]byte, bodyLen)
	body[0] = 1 // version 1
	for i := 0; i < 16; i++ {
		body[4+i] = byte(0xFF)
	}
	binary.BigEndian.PutUint32(body[20:24], uint32(kidCount))
	// KID 1
	for i := 0; i < 16; i++ {
		body[24+i] = byte(1)
	}
	// KID 2
	for i := 0; i < 16; i++ {
		body[40+i] = byte(2)
	}
	binary.BigEndian.PutUint32(body[56:60], 4) // data length
	copy(body[60:], []byte{0xDE, 0xAD, 0xBE, 0xEF})

	pssh, err := ParsePssh(body)
	if err != nil {
		t.Fatalf("ParsePssh: %v", err)
	}
	if len(pssh.KIDs) != 2 {
		t.Errorf("KIDs len = %d, want 2", len(pssh.KIDs))
	}
	if pssh.KIDs[0][0] != 1 || pssh.KIDs[1][0] != 2 {
		t.Error("KID values mismatch")
	}
	if len(pssh.Data) != 4 {
		t.Errorf("Data len = %d, want 4", len(pssh.Data))
	}
}

func TestParsePsshTooShort(t *testing.T) {
	_, err := ParsePssh(make([]byte, 10))
	if err == nil {
		t.Error("expected error for short body")
	}
}

// --- DecryptCENC tests ---

func TestDecryptCENC_CTR(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	iv := make([]byte, 16)

	// Encrypt with CTR
	plaintext := []byte("Hello, CENC-CTR mode! This is a test message.")
	block, _ := aes.NewCipher(key)
	stream := cipher.NewCTR(block, iv)
	ciphertext := make([]byte, len(plaintext))
	stream.XORKeyStream(ciphertext, plaintext)

	// Decrypt with our function
	decrypted, err := DecryptCENC(ciphertext, key, iv, "cenc")
	if err != nil {
		t.Fatalf("DecryptCENC: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted text doesn't match plaintext")
	}
}

func TestDecryptCENC_CBC(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	iv := make([]byte, 16)

	// Encrypt with CBC (PKCS7 padding)
	plaintext := []byte("Hello, CENC-CBC mode!") // 21 bytes, not block-aligned
	padded := pkcs7Pad(plaintext, aes.BlockSize)

	block, _ := aes.NewCipher(key)
	stream := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	stream.CryptBlocks(ciphertext, padded)

	// Decrypt
	decrypted, err := DecryptCENC(ciphertext, key, iv, "cbc")
	if err != nil {
		t.Fatalf("DecryptCENC: %v", err)
	}
	// Remove padding
	decrypted = pkcs7Unpad(decrypted)
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptCENC_EmptyKey(t *testing.T) {
	_, err := DecryptCENC([]byte("test"), nil, make([]byte, 16), "cenc")
	if err == nil {
		t.Error("expected error for empty key")
	}
}

func TestDecryptCENC_UnsupportedMode(t *testing.T) {
	_, err := DecryptCENC([]byte("test"), make([]byte, 16), make([]byte, 16), "xyz")
	if err == nil {
		t.Error("expected error for unsupported mode")
	}
}

func TestDecryptCENC_InvalidIV(t *testing.T) {
	key := make([]byte, 16)
	_, err := DecryptCENC([]byte("test"), key, make([]byte, 8), "cenc")
	if err == nil {
		t.Error("expected error for 8-byte IV in CTR mode")
	}
}

func TestDecryptCENC_CBCNotAligned(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	_, err := DecryptCENC(make([]byte, 13), key, iv, "cbc")
	if err == nil {
		t.Error("expected error for non-block-aligned CBC data")
	}
}

// --- DecryptCENCSubSample tests ---

func TestDecryptCENCSubSample_CTR(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i + 1)
	}
	iv := make([]byte, 16)

	// Create test data: 10 clear bytes + 32 encrypted bytes + 10 clear bytes
	clear1 := make([]byte, 10)
	for i := range clear1 {
		clear1[i] = byte(0xAA)
	}
	plaintext := make([]byte, 32)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	clear2 := make([]byte, 10)
	for i := range clear2 {
		clear2[i] = byte(0xBB)
	}

	// Encrypt the middle portion
	block, _ := aes.NewCipher(key)
	stream := cipher.NewCTR(block, iv)
	encrypted := make([]byte, len(plaintext))
	stream.XORKeyStream(encrypted, plaintext)

	// Build input data
	data := make([]byte, 0, len(clear1)+len(encrypted)+len(clear2))
	data = append(data, clear1...)
	data = append(data, encrypted...)
	data = append(data, clear2...)

	subsamples := []SubsampleEntry{
		{ClearBytes: 10, EncryptedBytes: 32},
		{ClearBytes: 10, EncryptedBytes: 0},
	}

	result, err := DecryptCENCSubSample(data, key, iv, "cenc", subsamples)
	if err != nil {
		t.Fatalf("DecryptCENCSubSample: %v", err)
	}

	// Verify clear portions are unchanged
	if !bytes.Equal(result[0:10], clear1) {
		t.Error("first clear portion modified")
	}
	if !bytes.Equal(result[42:52], clear2) {
		t.Error("second clear portion modified")
	}

	// Verify encrypted portion was decrypted
	if !bytes.Equal(result[10:42], plaintext) {
		t.Error("encrypted portion not correctly decrypted")
	}
}

// --- ParseSubsampleSizes tests ---

func TestParseSubsampleSizes(t *testing.T) {
	data := make([]byte, 12) // 2 entries × 6 bytes
	binary.BigEndian.PutUint16(data[0:2], 100)    // clear bytes
	binary.BigEndian.PutUint32(data[2:6], 2000)   // encrypted bytes
	binary.BigEndian.PutUint16(data[6:8], 50)     // clear bytes
	binary.BigEndian.PutUint32(data[8:12], 1000)  // encrypted bytes

	entries, err := ParseSubsampleSizes(data)
	if err != nil {
		t.Fatalf("ParseSubsampleSizes: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	if entries[0].ClearBytes != 100 || entries[0].EncryptedBytes != 2000 {
		t.Errorf("entry[0] = {%d, %d}, want {100, 2000}", entries[0].ClearBytes, entries[0].EncryptedBytes)
	}
	if entries[1].ClearBytes != 50 || entries[1].EncryptedBytes != 1000 {
		t.Errorf("entry[1] = {%d, %d}, want {50, 1000}", entries[1].ClearBytes, entries[1].EncryptedBytes)
	}
}

func TestParseSubsampleSizesNotAligned(t *testing.T) {
	_, err := ParseSubsampleSizes(make([]byte, 7))
	if err == nil {
		t.Error("expected error for non-aligned data")
	}
}

// --- ParseSenc tests ---

func TestParseSencWithSubsamples(t *testing.T) {
	// senc: version(1) + flags(3) + sample_count(4) + IV(16) + subsample_count(2) + subsample(6)
	body := make([]byte, 8+16+2+6)
	body[0] = 0         // version
	body[3] = 0x02      // flag: subsample encryption
	binary.BigEndian.PutUint32(body[4:8], 1) // sample count
	// IV
	for i := 0; i < 16; i++ {
		body[8+i] = byte(i)
	}
	// Subsample count
	binary.BigEndian.PutUint16(body[24:26], 1)
	// Subsample: clear=10, encrypted=100
	binary.BigEndian.PutUint16(body[26:28], 10)
	binary.BigEndian.PutUint32(body[28:32], 100)

	subsamples, iv, sampleCount := parseSenc(body)
	if sampleCount != 1 {
		t.Errorf("sampleCount = %d, want 1", sampleCount)
	}
	if len(iv) != 16 {
		t.Fatalf("IV len = %d, want 16", len(iv))
	}
	if iv[0] != 0 || iv[15] != 15 {
		t.Error("IV values mismatch")
	}
	if len(subsamples) != 1 {
		t.Fatalf("subsamples len = %d, want 1", len(subsamples))
	}
	if subsamples[0].ClearBytes != 10 || subsamples[0].EncryptedBytes != 100 {
		t.Errorf("subsample = {%d, %d}, want {10, 100}", subsamples[0].ClearBytes, subsamples[0].EncryptedBytes)
	}
}

func TestParseSencNoSubsamples(t *testing.T) {
	body := make([]byte, 8+16)
	body[0] = 0 // version, no subsample flag
	binary.BigEndian.PutUint32(body[4:8], 1)

	subsamples, iv, _ := parseSenc(body)
	if len(subsamples) != 0 {
		t.Errorf("subsamples len = %d, want 0", len(subsamples))
	}
	if len(iv) != 16 {
		t.Errorf("IV len = %d, want 16", len(iv))
	}
}

// --- ParseMoofEncryption tests ---

func TestParseMoofEncryption(t *testing.T) {
	// Build a minimal moof with traf containing senc
	sencBody := make([]byte, 8+16)
	sencBody[0] = 0
	binary.BigEndian.PutUint32(sencBody[4:8], 1)
	sencBox := makeBox("senc", sencBody)

	trafBox := makeBox("traf", sencBox)
	moofBox := makeBox("moof", trafBox)

	// Strip moof header to get body
	trafs, err := ParseMoofEncryption(moofBox[8:])
	if err != nil {
		t.Fatalf("ParseMoofEncryption: %v", err)
	}
	if len(trafs) != 1 {
		t.Fatalf("trafs len = %d, want 1", len(trafs))
	}
	if len(trafs[0].IV) != 16 {
		t.Errorf("IV len = %d, want 16", len(trafs[0].IV))
	}
}

// --- FindEncryptionBoxes tests ---

func TestFindEncryptionBoxes(t *testing.T) {
	// Build moov with pssh + trak containing sinf
	tencBody := make([]byte, 22)
	tencBody[0] = 1
	tencBody[4] = 1
	tencBody[5] = 8
	tencBox := makeBox("tenc", tencBody)
	schiBox := makeBox("schi", tencBox)

	schmBody := make([]byte, 12)
	copy(schmBody[4:8], []byte("cenc"))
	schmBox := makeBox("schm", schmBody)

	sinfBox := makeBox("sinf", append(schmBox, schiBox...))

	// Build a minimal stbl → minf → mdia → trak
	stblBox := makeBox("stbl", sinfBox)
	minfBox := makeBox("minf", stblBox)
	mdiaBox := makeBox("mdia", minfBox)
	trakBox := makeBox("trak", mdiaBox)

	// PSSH
	psshBody := make([]byte, 24)
	psshBody[0] = 0
	for i := 0; i < 16; i++ {
		psshBody[4+i] = byte(i)
	}
	psshBox := makeBox("pssh", psshBody)

	moovBody := append(psshBox, trakBox...)

	sinfs, psshs, err := FindEncryptionBoxes(moovBody)
	if err != nil {
		t.Fatalf("FindEncryptionBoxes: %v", err)
	}
	if len(sinfs) != 1 {
		t.Fatalf("sinfs len = %d, want 1", len(sinfs))
	}
	if sinfs[0].SchemeType != "cenc" {
		t.Errorf("SchemeType = %q, want cenc", sinfs[0].SchemeType)
	}
	if sinfs[0].TrackEncryption == nil {
		t.Fatal("expected TrackEncryption")
	}
	if len(psshs) != 1 {
		t.Fatalf("psshs len = %d, want 1", len(psshs))
	}
}

// --- DecryptCENC round-trip with random data ---

func TestDecryptCENC_CTR_RoundTrip(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	rand.Read(key)
	rand.Read(iv)

	// Random plaintext (not block-aligned — CTR handles any size)
	plaintext := make([]byte, 137)
	rand.Read(plaintext)

	// Encrypt
	block, _ := aes.NewCipher(key)
	stream := cipher.NewCTR(block, iv)
	ciphertext := make([]byte, len(plaintext))
	stream.XORKeyStream(ciphertext, plaintext)

	// Decrypt
	decrypted, err := DecryptCENC(ciphertext, key, iv, "cenc")
	if err != nil {
		t.Fatalf("DecryptCENC: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("round-trip CTR failed")
	}
}

func TestDecryptCENC_CTR_LargeData(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	rand.Read(key)
	rand.Read(iv)

	plaintext := make([]byte, 1024*1024) // 1MB
	rand.Read(plaintext)

	block, _ := aes.NewCipher(key)
	stream := cipher.NewCTR(block, iv)
	ciphertext := make([]byte, len(plaintext))
	stream.XORKeyStream(ciphertext, plaintext)

	decrypted, err := DecryptCENC(ciphertext, key, iv, "cenc")
	if err != nil {
		t.Fatalf("DecryptCENC: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("large data CTR round-trip failed")
	}
}

// --- Helper functions ---

func makeBox(boxType string, body []byte) []byte {
	size := uint32(8 + len(body))
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:4], size)
	copy(box[4:8], boxType)
	copy(box[8:], body)
	return box
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

func pkcs7Unpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	padding := int(data[len(data)-1])
	if padding > len(data) || padding > aes.BlockSize {
		return data
	}
	return data[:len(data)-padding]
}

// Verify hex key/IV matching (common in real CENC scenarios)
func TestDecryptCENC_KnownVector(t *testing.T) {
	// Known test vector: key=00112233445566778899aabbccddeeff, iv=00000000000000000000000000000000
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	iv := make([]byte, 16)

	plaintext := []byte("test data for CTR mode 1234567890ab")

	block, _ := aes.NewCipher(key)
	stream := cipher.NewCTR(block, iv)
	ciphertext := make([]byte, len(plaintext))
	stream.XORKeyStream(ciphertext, plaintext)

	decrypted, err := DecryptCENC(ciphertext, key, iv, "cenc")
	if err != nil {
		t.Fatalf("DecryptCENC: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

// Test CBCS mode
func TestDecryptCENC_CBSC(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := make([]byte, 48) // 3 blocks, block-aligned
	for i := range plaintext {
		plaintext[i] = byte(i)
	}

	block, _ := aes.NewCipher(key)
	stream := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(plaintext))
	stream.CryptBlocks(ciphertext, plaintext)

	decrypted, err := DecryptCENC(ciphertext, key, iv, "cbcs")
	if err != nil {
		t.Fatalf("DecryptCENC cbcs: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("cbcs round-trip failed")
	}
}
