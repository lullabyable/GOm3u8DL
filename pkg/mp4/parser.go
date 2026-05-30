package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Box represents an MP4 Box (atom).
type Box struct {
	Type   [4]byte
	Size   uint64
	Offset int64
	Body   []byte // raw body (for leaf boxes)
	Reader io.Reader
}

// BoxType returns the box type as a string.
func (b *Box) BoxType() string {
	return string(b.Type[:])
}

// IsContainer returns true if this box type typically contains child boxes.
func (b *Box) IsContainer() bool {
	switch b.BoxType() {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf",
		"mvex", "moof", "traf", "sinf", "schi", "rinf", "hnti",
		"hinf", "strk", "strd":
		return true
	}
	return false
}

// HeaderSize returns the size of the box header (8 or 16 bytes).
func (b *Box) HeaderSize() int {
	if b.Size == 1 {
		return 16 // extended size
	}
	return 8
}

// Parser reads and parses MP4 boxes from a stream.
type Parser struct {
	reader io.ReadSeeker
}

// NewParser creates a new MP4 parser.
func NewParser(r io.ReadSeeker) *Parser {
	return &Parser{reader: r}
}

// ReadBox reads the next box from the stream.
func (p *Parser) ReadBox() (*Box, error) {
	offset, err := p.reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	// Read box header
	header := make([]byte, 8)
	if _, err := io.ReadFull(p.reader, header); err != nil {
		return nil, err
	}

	box := &Box{Offset: offset}
	box.Size = uint64(binary.BigEndian.Uint32(header[0:4]))
	copy(box.Type[:], header[4:8])

	if box.Size == 0 {
		// Box extends to end of file
		end, err := p.reader.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		box.Size = uint64(end - offset)
		p.reader.Seek(offset+8, io.SeekStart)
	} else if box.Size == 1 {
		// Extended size (64-bit)
		extSize := make([]byte, 8)
		if _, err := io.ReadFull(p.reader, extSize); err != nil {
			return nil, err
		}
		box.Size = binary.BigEndian.Uint64(extSize)
	}

	// Read body for leaf boxes
	bodyStart := int64(box.HeaderSize())
	bodySize := int64(box.Size) - bodyStart

	if bodySize > 0 && bodySize < 10*1024*1024 { // cap at 10MB for safety
		box.Body = make([]byte, bodySize)
		if _, err := io.ReadFull(p.reader, box.Body); err != nil {
			return nil, fmt.Errorf("read box %s body: %w", box.BoxType(), err)
		}
	} else if bodySize > 0 {
		// Skip large boxes
		if _, err := p.reader.Seek(bodySize, io.SeekCurrent); err != nil {
			return nil, err
		}
	}

	return box, nil
}

// SkipBox skips to the end of the current box.
func (p *Parser) SkipBox(box *Box) error {
	target := box.Offset + int64(box.Size)
	_, err := p.reader.Seek(target, io.SeekStart)
	return err
}

// ReadChildBoxes reads all child boxes of a container box.
func (p *Parser) ReadChildBoxes(body []byte) ([]*Box, error) {
	var boxes []*Box
	r := &byteReader{data: body, pos: 0}

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
			if _, err := r.Read(box.Body); err != nil {
				break
			}
		}

		boxes = append(boxes, box)
	}

	return boxes, nil
}

// byteReader wraps a byte slice as an io.Reader.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *byteReader) Remaining() int {
	return len(r.data) - r.pos
}

// ParseBoxHeader parses a box header and returns type + size.
func ParseBoxHeader(data []byte) (boxType string, size uint64, headerLen int) {
	if len(data) < 8 {
		return "", 0, 0
	}
	size = uint64(binary.BigEndian.Uint32(data[0:4]))
	boxType = string(data[4:8])
	headerLen = 8

	if size == 1 && len(data) >= 16 {
		size = binary.BigEndian.Uint64(data[8:16])
		headerLen = 16
	}
	return
}
