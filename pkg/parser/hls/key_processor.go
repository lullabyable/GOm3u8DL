package hls

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// parseKeyTag parses an #EXT-X-KEY line and returns an EncryptInfo.
// Example: #EXT-X-KEY:METHOD=AES-128,URI="https://example.com/key.bin",IV=0x1234...
func parseKeyTag(baseURL, line string) (model.EncryptInfo, error) {
	attrs, err := parseAttributes(line)
	if err != nil {
		return model.EncryptInfo{}, err
	}

	info := model.EncryptInfo{}

	method := strings.ToUpper(attrs["METHOD"])
	switch method {
	case MethodNone:
		info.Method = model.EncryptMethodNone
	case MethodAES128:
		info.Method = model.EncryptMethodAES128
	case MethodAES128ECB:
		info.Method = model.EncryptMethodAES128ECB
	case MethodSampleAES:
		info.Method = model.EncryptMethodSampleAES
	case MethodSampleAESCTR:
		info.Method = model.EncryptMethodSampleAESCTR
	default:
		return model.EncryptInfo{}, fmt.Errorf("unknown encryption method: %s", method)
	}

	if uri, ok := attrs["URI"]; ok {
		info.KeyURL = resolveURL(baseURL, uri)
	}

	if iv, ok := attrs["IV"]; ok {
		iv = strings.TrimPrefix(iv, "0x")
		iv = strings.TrimPrefix(iv, "0X")
		b, err := hex.DecodeString(iv)
		if err != nil {
			return model.EncryptInfo{}, fmt.Errorf("invalid IV: %w", err)
		}
		info.IV = b
	}

	if fmt, ok := attrs["KEYFORMAT"]; ok {
		info.KeyFormat = fmt
	}

	return info, nil
}
