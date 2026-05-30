package mp4

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// InitInfo holds parsed information from an fMP4 init segment.
type InitInfo struct {
	HasVideo   bool
	HasAudio   bool
	VideoCodec string
	AudioCodec string
	Width      uint16
	Height     uint16
	Timescale  uint32
	Duration   uint32
	TrackID    uint32
}

// ParseInitSegment parses an fMP4 init segment (ftyp + moov).
func ParseInitSegment(data []byte) (*InitInfo, error) {
	info := &InitInfo{}

	boxes, err := parseTopLevelBoxes(data)
	if err != nil {
		return nil, err
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "moov":
			parseMoov(box.Body, info)
		}
	}

	return info, nil
}

func parseTopLevelBoxes(data []byte) ([]*Box, error) {
	r := &byteReader{data: data, pos: 0}
	parser := &Parser{reader: nil}

	// We'll manually parse from the byte reader
	var boxes []*Box
	for r.Remaining() >= 8 {
		offset := int64(r.pos)
		header := make([]byte, 8)
		if _, err := r.Read(header); err != nil {
			break
		}

		box := &Box{Offset: offset}
		box.Size = uint64(binary.BigEndian.Uint32(header[0:4]))
		copy(box.Type[:], header[4:8])

		if box.Size == 0 || box.Size > uint64(r.Remaining()+8) {
			break
		}

		bodySize := int64(box.Size) - 8
		if bodySize > 0 {
			box.Body = make([]byte, bodySize)
			r.Read(box.Body)
		}

		boxes = append(boxes, box)
	}
	_ = parser
	return boxes, nil
}

func parseMoov(body []byte, info *InitInfo) {
	boxes, _ := parseBoxesFromBytes(body)
	for _, box := range boxes {
		switch box.BoxType() {
		case "mvhd":
			parseMvhd(box.Body, info)
		case "trak":
			parseTrak(box.Body, info)
		}
	}
}

func parseMvhd(body []byte, info *InitInfo) {
	if len(body) < 4 {
		return
	}
	version := body[0]
	var offset int
	if version == 0 {
		offset = 4 + 4 + 4 // version(1) + flags(3) + creation(4) + modification(4) = skip to timescale
	} else {
		offset = 4 + 8 + 8 // version(1) + flags(3) + creation(8) + modification(8)
	}

	if len(body) >= offset+8 {
		info.Timescale = binary.BigEndian.Uint32(body[offset : offset+4])
		info.Duration = binary.BigEndian.Uint32(body[offset+4 : offset+8])
	}
}

func parseTrak(body []byte, info *InitInfo) {
	boxes, _ := parseBoxesFromBytes(body)
	for _, box := range boxes {
		switch box.BoxType() {
		case "tkhd":
			parseTkhd(box.Body, info)
		case "mdia":
			parseMdia(box.Body, info)
		}
	}
}

func parseTkhd(body []byte, info *InitInfo) {
	if len(body) < 4 {
		return
	}
	version := body[0]
	var idOffset int
	if version == 0 {
		idOffset = 4 + 4 + 4 + 4 + 4 + 8 + 8 + 2 + 2 + 2 + 2
	} else {
		idOffset = 4 + 8 + 8 + 8 + 8 + 2 + 2 + 2 + 2
	}

	if len(body) >= idOffset+4 {
		info.TrackID = binary.BigEndian.Uint32(body[idOffset : idOffset+4])
	}

	// Parse width/height (fixed-point 16.16) at end of tkhd
	var whOffset int
	if version == 0 {
		whOffset = 4 + 4 + 4 + 4 + 4 + 8 + 8 + 2 + 2 + 2 + 2 + 4 + 36
	} else {
		whOffset = 4 + 8 + 8 + 8 + 8 + 2 + 2 + 2 + 2 + 4 + 36
	}

	if len(body) >= whOffset+8 {
		info.Width = uint16(binary.BigEndian.Uint32(body[whOffset:whOffset+4]) >> 16)
		info.Height = uint16(binary.BigEndian.Uint32(body[whOffset+4:whOffset+8]) >> 16)
		if info.Width > 0 {
			info.HasVideo = true
		}
	}
}

func parseMdia(body []byte, info *InitInfo) {
	boxes, _ := parseBoxesFromBytes(body)
	for _, box := range boxes {
		switch box.BoxType() {
		case "mdhd":
			parseMdhd(box.Body, info)
		case "minf":
			parseMinf(box.Body, info)
		}
	}
}

func parseMdhd(body []byte, info *InitInfo) {
	if len(body) < 4 {
		return
	}
	version := body[0]
	var offset int
	if version == 0 {
		offset = 4 + 4 + 4
	} else {
		offset = 4 + 8 + 8
	}
	if len(body) >= offset+8 {
		ts := binary.BigEndian.Uint32(body[offset : offset+4])
		if info.Timescale == 0 {
			info.Timescale = ts
		}
	}
}

func parseMinf(body []byte, info *InitInfo) {
	boxes, _ := parseBoxesFromBytes(body)
	for _, box := range boxes {
		if box.BoxType() == "stbl" {
			parseStbl(box.Body, info)
		}
	}
}

func parseStbl(body []byte, info *InitInfo) {
	boxes, _ := parseBoxesFromBytes(body)
	for _, box := range boxes {
		switch box.BoxType() {
		case "stsd":
			parseStsd(box.Body, info)
		}
	}
}

func parseStsd(body []byte, info *InitInfo) {
	// stsd: version(1) + flags(3) + entry_count(4) + entries...
	if len(body) < 8 {
		return
	}
	offset := 8 // skip version+flags + entry_count

	for offset < len(body) {
		if offset+8 > len(body) {
			break
		}
		size := binary.BigEndian.Uint32(body[offset : offset+4])
		codec := string(body[offset+4 : offset+8])

		if size == 0 {
			break
		}

		codecStr := strings.TrimRight(codec, "\x00")
		switch {
		case strings.HasPrefix(codecStr, "avc") || strings.HasPrefix(codecStr, "hev") ||
			strings.HasPrefix(codecStr, "hvc") || codecStr == "mp4v":
			info.VideoCodec = codecStr
			info.HasVideo = true
		case strings.HasPrefix(codecStr, "mp4a") || strings.HasPrefix(codecStr, "ac-") ||
			strings.HasPrefix(codecStr, "ec-") || codecStr == "Opus":
			info.AudioCodec = codecStr
			info.HasAudio = true
		}

		offset += int(size)
	}
}

// parseBoxesFromBytes parses child boxes from a byte slice.
func parseBoxesFromBytes(data []byte) ([]*Box, error) {
	var boxes []*Box
	r := &byteReader{data: data, pos: 0}

	for r.Remaining() >= 8 {
		offset := int64(r.pos)
		header := make([]byte, 8)
		if _, err := r.Read(header); err != nil {
			break
		}

		box := &Box{Offset: offset}
		box.Size = uint64(binary.BigEndian.Uint32(header[0:4]))
		copy(box.Type[:], header[4:8])

		if box.Size == 0 || box.Size > uint64(r.Remaining()+8) {
			break
		}

		bodySize := int64(box.Size) - 8
		if bodySize > 0 {
			box.Body = make([]byte, bodySize)
			r.Read(box.Body)
		}

		boxes = append(boxes, box)
	}

	return boxes, nil
}

// String returns a debug string for InitInfo.
func (info *InitInfo) String() string {
	return fmt.Sprintf("InitInfo{video=%v(%s) audio=%v(%s) %dx%d ts=%d dur=%d track=%d}",
		info.HasVideo, info.VideoCodec, info.HasAudio, info.AudioCodec,
		info.Width, info.Height, info.Timescale, info.Duration, info.TrackID)
}
