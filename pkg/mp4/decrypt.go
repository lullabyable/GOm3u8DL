package mp4

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
)

// CENC encryption scheme constants.
const (
	SchemeTypeCENC  = "cenc"  // AES-CTR with constant IV
	SchemeTypeCBCS  = "cbcs"  // AES-CBC with pattern encryption (not fully supported)
	SchemeTypeCENS  = "cens"  // AES-CTR with pattern encryption (not fully supported)
)

// TrackEncryption holds parsed tenc box data.
type TrackEncryption struct {
	IsEncrypted bool
	IVSize      byte
	KID         [16]byte
}

// ParseTenc parses a tenc (Track Encryption) box body.
//
// tenc layout (full box):
//   version(1) + flags(3) + reserved(1) + default_IsEncrypted(1) + default_IV_size(1) + default_KID(16)
//
// For version 0, there's an extra reserved byte before default_KID.
func ParseTenc(body []byte) (*TrackEncryption, error) {
	if len(body) < 20 {
		return nil, errors.New("tenc: body too short")
	}

	version := body[0]
	// flags := uint32(body[1])<<16 | uint32(body[2])<<8 | uint32(body[3])

	tenc := &TrackEncryption{}

	if version == 0 {
		// version 0: reserved(4) + default_IsEncrypted(1) + default_IV_size(1) + reserved(1) + default_KID(16)
		if len(body) < 20 {
			return nil, errors.New("tenc v0: body too short")
		}
		tenc.IsEncrypted = body[4+4] != 0
		tenc.IVSize = body[4+5]
		// body[4+6] is reserved
		copy(tenc.KID[:], body[4+7:4+7+16])
	} else {
		// version 1+: reserved(4) + default_IsEncrypted(1) + default_IV_size(1) + default_KID(16)
		if len(body) < 22 {
			return nil, errors.New("tenc v1: body too short")
		}
		tenc.IsEncrypted = body[4] != 0
		tenc.IVSize = body[5]
		copy(tenc.KID[:], body[6:6+16])
	}

	return tenc, nil
}

// EncryptionInfo holds encryption parameters extracted from sinf/schm/schi/tenc.
type EncryptionInfo struct {
	SchemeType    string
	SchemeVersion uint32
	TrackEncryption *TrackEncryption
}

// ParseSinf parses a sinf (Sample Encryption Information) box.
// It looks for schm (scheme) and schi (scheme information) child boxes.
func ParseSinf(body []byte) (*EncryptionInfo, error) {
	boxes, err := parseBoxesFromBytes(body)
	if err != nil {
		return nil, fmt.Errorf("sinf: %w", err)
	}

	info := &EncryptionInfo{}

	for _, box := range boxes {
		switch box.BoxType() {
		case "schm":
			info.SchemeType, info.SchemeVersion = parseSchm(box.Body)
		case "schi":
			tenc, err := parseSchiTenc(box.Body)
			if err == nil {
				info.TrackEncryption = tenc
			}
		}
	}

	return info, nil
}

// parseSchm parses a scheme type box.
// schm layout: version(1) + flags(3) + scheme_type(4) + scheme_version(4)
func parseSchm(body []byte) (string, uint32) {
	if len(body) < 12 {
		return "", 0
	}
	// Skip version(1) + flags(3)
	schemeType := string(body[4:8])
	schemeVersion := binary.BigEndian.Uint32(body[8:12])
	return schemeType, schemeVersion
}

// parseSchiTenc parses schi child boxes looking for tenc.
func parseSchiTenc(body []byte) (*TrackEncryption, error) {
	boxes, err := parseBoxesFromBytes(body)
	if err != nil {
		return nil, err
	}

	for _, box := range boxes {
		if box.BoxType() == "tenc" {
			return ParseTenc(box.Body)
		}
	}
	return nil, errors.New("schi: no tenc found")
}

// ParsePssh holds parsed PSSH box data.
type PsshBox struct {
	SystemID  [16]byte
	KIDs      [][16]byte
	Data      []byte
}

// ParsePssh parses a pssh (Protection System Specific Header) box.
func ParsePssh(body []byte) (*PsshBox, error) {
	if len(body) < 24 {
		return nil, errors.New("pssh: body too short")
	}

	version := body[0]

	pssh := &PsshBox{}
	copy(pssh.SystemID[:], body[4:20])

	if version == 0 {
		dataLen := binary.BigEndian.Uint32(body[20:24])
		if uint32(len(body)) >= 24+dataLen {
			pssh.Data = make([]byte, dataLen)
			copy(pssh.Data, body[24:24+dataLen])
		}
	} else if version == 1 {
		if len(body) < 28 {
			return nil, errors.New("pssh v1: body too short")
		}
		kidCount := binary.BigEndian.Uint32(body[20:24])
		offset := 24
		for i := uint32(0); i < kidCount; i++ {
			if offset+16 > len(body) {
				break
			}
			var kid [16]byte
			copy(kid[:], body[offset:offset+16])
			pssh.KIDs = append(pssh.KIDs, kid)
			offset += 16
		}
		if offset+4 <= len(body) {
			dataLen := binary.BigEndian.Uint32(body[offset : offset+4])
			offset += 4
			if offset+int(dataLen) <= len(body) {
				pssh.Data = make([]byte, dataLen)
				copy(pssh.Data, body[offset:offset+int(dataLen)])
			}
		}
	}

	return pssh, nil
}

// DecryptCENC decrypts CENC-encrypted sample data.
//
// mode must be one of:
//   - "cenc": AES-128-CTR mode (standard CENC)
//   - "cbcs": AES-128-CBC mode with pattern (simplified — full CBCS requires subsample handling)
//   - "cbc":  AES-128-CBC mode (legacy, used by some implementations)
//
// For CTR mode: key must be 16 bytes, iv must be 16 bytes.
// For CBC mode: key must be 16 bytes, iv must be 16 bytes.
// Data must be a multiple of 16 bytes for CBC mode.
func DecryptCENC(data []byte, key []byte, iv []byte, mode string) ([]byte, error) {
	if len(key) == 0 {
		return nil, errors.New("decrypt: key is empty")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: NewCipher: %w", err)
	}

	switch mode {
	case "cenc", "cens":
		return decryptCTR(block, data, iv)
	case "cbcs", "cbc":
		return decryptCBC(block, data, iv)
	default:
		return nil, fmt.Errorf("decrypt: unsupported mode %q", mode)
	}
}

// DecryptCENCSubSample decrypts CENC-encrypted data with subsample encryption.
// In subsample encryption, only certain portions of the data are encrypted.
//
// subsampleSizes contains pairs: (clearBytes, encryptedBytes) repeated.
// The data is laid out as: [clear][encrypted][clear][encrypted]...
//
// For CTR mode, the encrypted portions are decrypted in-place.
// For CBC mode, the encrypted portions are decrypted with PKCS7 padding handling.
func DecryptCENCSubSample(data []byte, key []byte, iv []byte, mode string, subsampleSizes []SubsampleEntry) ([]byte, error) {
	if len(key) == 0 {
		return nil, errors.New("decrypt: key is empty")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: NewCipher: %w", err)
	}

	result := make([]byte, len(data))
	copy(result, data)

	offset := 0
	for _, entry := range subsampleSizes {
		// Clear portion — skip
		clearLen := int(entry.ClearBytes)
		if offset+clearLen > len(result) {
			return nil, fmt.Errorf("decrypt: subsample clear overflow at offset %d", offset)
		}
		offset += clearLen

		// Encrypted portion — decrypt
		encLen := int(entry.EncryptedBytes)
		if offset+encLen > len(result) {
			return nil, fmt.Errorf("decrypt: subsample encrypted overflow at offset %d", offset)
		}

		if encLen > 0 {
			chunk := result[offset : offset+encLen]
			switch mode {
			case "cenc", "cens":
				stream := cipher.NewCTR(block, iv)
				stream.XORKeyStream(chunk, chunk)
				// Update IV for next subsample (CTR counter continues)
				iv = incrementIV(iv, encLen)
			case "cbcs", "cbc":
				// CBC requires full block alignment; for simplicity, process all at once
				// Real CBCS uses pattern encryption which is more complex
				if encLen%aes.BlockSize != 0 {
					return nil, fmt.Errorf("decrypt: CBC encrypted portion not block-aligned (%d bytes)", encLen)
				}
				stream := cipher.NewCBCDecrypter(block, iv)
				stream.CryptBlocks(chunk, chunk)
				// Use last ciphertext block as next IV
				if encLen >= aes.BlockSize {
					copy(iv, chunk[encLen-aes.BlockSize:encLen])
				}
			default:
				return nil, fmt.Errorf("decrypt: unsupported mode %q", mode)
			}
		}

		offset += encLen
	}

	return result, nil
}

// SubsampleEntry represents a clear/encrypted byte pair in subsample encryption.
type SubsampleEntry struct {
	ClearBytes     uint16
	EncryptedBytes uint32
}

// ParseSubsampleSizes parses subsample size entries from raw bytes.
// Each entry: clearBytes(2) + encryptedBytes(4)
func ParseSubsampleSizes(data []byte) ([]SubsampleEntry, error) {
	if len(data)%6 != 0 {
		return nil, errors.New("subsample data not aligned to 6 bytes")
	}

	count := len(data) / 6
	entries := make([]SubsampleEntry, count)
	for i := 0; i < count; i++ {
		off := i * 6
		entries[i].ClearBytes = binary.BigEndian.Uint16(data[off : off+2])
		entries[i].EncryptedBytes = binary.BigEndian.Uint32(data[off+2 : off+6])
	}
	return entries, nil
}

// decryptCTR decrypts data using AES-128-CTR.
func decryptCTR(block cipher.Block, data []byte, iv []byte) ([]byte, error) {
	if len(iv) != 16 {
		return nil, errors.New("CTR: IV must be 16 bytes")
	}

	result := make([]byte, len(data))
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(result, data)
	return result, nil
}

// decryptCBC decrypts data using AES-128-CBC.
func decryptCBC(block cipher.Block, data []byte, iv []byte) ([]byte, error) {
	if len(iv) != aes.BlockSize {
		return nil, errors.New("CBC: IV must be 16 bytes")
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("CBC: data must be block-aligned")
	}

	result := make([]byte, len(data))
	stream := cipher.NewCBCDecrypter(block, iv)
	stream.CryptBlocks(result, data)
	return result, nil
}

// incrementIV increments the IV by the given number of blocks for CTR mode continuation.
func incrementIV(iv []byte, byteCount int) []byte {
	newIV := make([]byte, len(iv))
	copy(newIV, iv)

	blocks := byteCount / aes.BlockSize
	if byteCount%aes.BlockSize != 0 {
		blocks++
	}

	// Increment the counter portion (last 8 bytes of a 16-byte IV)
	carry := uint64(blocks)
	for i := len(newIV) - 1; i >= 0 && carry > 0; i-- {
		sum := uint64(newIV[i]) + (carry & 0xff)
		newIV[i] = byte(sum)
		carry >>= 8
		if i >= len(newIV)-8 {
			carry += (sum >> 8)
		}
	}

	return newIV
}

// FindEncryptionBoxes scans moov/trak boxes to find encryption-related boxes.
// Returns all sinf and pssh boxes found in the moov hierarchy.
func FindEncryptionBoxes(moovBody []byte) (sinfs []EncryptionInfo, psshs []PsshBox, err error) {
	boxes, err := parseBoxesFromBytes(moovBody)
	if err != nil {
		return nil, nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "pssh":
			pssh, err := ParsePssh(box.Body)
			if err == nil {
				psshs = append(psshs, *pssh)
			}
		case "trak":
			tSinfs, tPsshs, _ := findEncryptionInTrak(box.Body)
			sinfs = append(sinfs, tSinfs...)
			psshs = append(psshs, tPsshs...)
		}
	}
	return
}

func findEncryptionInTrak(trakBody []byte) (sinfs []EncryptionInfo, psshs []PsshBox, err error) {
	boxes, err := parseBoxesFromBytes(trakBody)
	if err != nil {
		return nil, nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "mdia":
			tSinfs, tPsshs, _ := findEncryptionInMdia(box.Body)
			sinfs = append(sinfs, tSinfs...)
			psshs = append(psshs, tPsshs...)
		}
	}
	return
}

func findEncryptionInMdia(mdiaBody []byte) (sinfs []EncryptionInfo, psshs []PsshBox, err error) {
	boxes, err := parseBoxesFromBytes(mdiaBody)
	if err != nil {
		return nil, nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "minf":
			tSinfs, tPsshs, _ := findEncryptionInMinf(box.Body)
			sinfs = append(sinfs, tSinfs...)
			psshs = append(psshs, tPsshs...)
		}
	}
	return
}

func findEncryptionInMinf(minfBody []byte) (sinfs []EncryptionInfo, psshs []PsshBox, err error) {
	boxes, err := parseBoxesFromBytes(minfBody)
	if err != nil {
		return nil, nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "stbl":
			tSinfs, tPsshs, _ := findEncryptionInStbl(box.Body)
			sinfs = append(sinfs, tSinfs...)
			psshs = append(psshs, tPsshs...)
		}
	}
	return
}

func findEncryptionInStbl(stblBody []byte) (sinfs []EncryptionInfo, psshs []PsshBox, err error) {
	boxes, err := parseBoxesFromBytes(stblBody)
	if err != nil {
		return nil, nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "sinf":
			sinf, err := ParseSinf(box.Body)
			if err == nil {
				sinfs = append(sinfs, *sinf)
			}
		case "pssh":
			pssh, err := ParsePssh(box.Body)
			if err == nil {
				psshs = append(psshs, *pssh)
			}
		}
	}
	return
}

// ParseMoofEncryption parses encryption info from moof/traf boxes.
// Returns subsample sizes and encryption parameters for each traf.
type TrafEncryption struct {
	Subsamples []SubsampleEntry
	IV         []byte
	SampleCount uint32
}

func ParseMoofEncryption(moofBody []byte) ([]TrafEncryption, error) {
	boxes, err := parseBoxesFromBytes(moofBody)
	if err != nil {
		return nil, err
	}

	var trafs []TrafEncryption
	for _, box := range boxes {
		if box.BoxType() == "traf" {
			te, err := parseTrafEncryption(box.Body)
			if err == nil {
				trafs = append(trafs, *te)
			}
		}
	}
	return trafs, nil
}

func parseTrafEncryption(trafBody []byte) (*TrafEncryption, error) {
	boxes, err := parseBoxesFromBytes(trafBody)
	if err != nil {
		return nil, err
	}

	te := &TrafEncryption{}

	for _, box := range boxes {
		switch box.BoxType() {
		case "tfhd":
			parseTfhd(box.Body)
		case "senc":
			te.Subsamples, te.IV, te.SampleCount = parseSenc(box.Body)
		}
	}

	return te, nil
}

func parseTfhd(body []byte) {
	// Placeholder — tfhd parsing for track ID and default sample flags
}

// parseSenc parses a senc (Sample Encryption) box.
// senc layout: version(1) + flags(3) + sample_count(4) + IV(16)*count [+ subsample_info]
func parseSenc(body []byte) (subsamples []SubsampleEntry, iv []byte, sampleCount uint32) {
	if len(body) < 8 {
		return
	}

	version := body[0]
	flags := uint32(body[1])<<16 | uint32(body[2])<<8 | uint32(body[3])
	_ = version

	sampleCount = binary.BigEndian.Uint32(body[4:8])
	offset := 8

	// Read IVs (16 bytes each) — for simplicity, store first IV
	if sampleCount > 0 && offset+16 <= len(body) {
		iv = make([]byte, 16)
		copy(iv, body[offset:offset+16])
		offset += 16 * int(sampleCount)
	}

	// Parse subsample info if flag is set (bit 1)
	if flags&0x02 != 0 && offset < len(body) {
		// Each sample has its own subsample entries
		for i := uint32(0); i < sampleCount; i++ {
			if offset+2 > len(body) {
				break
			}
			subCount := binary.BigEndian.Uint16(body[offset : offset+2])
			offset += 2
			for j := uint16(0); j < subCount; j++ {
				if offset+6 > len(body) {
					break
				}
				entry := SubsampleEntry{
					ClearBytes:     binary.BigEndian.Uint16(body[offset : offset+2]),
					EncryptedBytes: binary.BigEndian.Uint32(body[offset+2 : offset+6]),
				}
				subsamples = append(subsamples, entry)
				offset += 6
			}
		}
	}

	return
}
