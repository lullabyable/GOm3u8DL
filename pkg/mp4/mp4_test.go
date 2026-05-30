package mp4

import (
	"testing"
)

func TestParseBoxHeader(t *testing.T) {
	// Standard box: size=100, type="moov"
	data := []byte{0, 0, 0, 0x64, 'm', 'o', 'o', 'v'}
	boxType, size, headerLen := ParseBoxHeader(data)
	if boxType != "moov" {
		t.Errorf("type = %q, want moov", boxType)
	}
	if size != 100 {
		t.Errorf("size = %d, want 100", size)
	}
	if headerLen != 8 {
		t.Errorf("headerLen = %d, want 8", headerLen)
	}
}

func TestParseBoxHeaderExtended(t *testing.T) {
	// Extended size: size=1 (indicates 64-bit), then 64-bit size
	data := []byte{0, 0, 0, 1, 'm', 'o', 'o', 'v', 0, 0, 0, 0, 0, 0, 1, 0}
	boxType, size, headerLen := ParseBoxHeader(data)
	if boxType != "moov" {
		t.Errorf("type = %q, want moov", boxType)
	}
	if size != 256 {
		t.Errorf("size = %d, want 256", size)
	}
	if headerLen != 16 {
		t.Errorf("headerLen = %d, want 16", headerLen)
	}
}

func TestBoxIsContainer(t *testing.T) {
	tests := []struct {
		boxType string
		want    bool
	}{
		{"moov", true},
		{"trak", true},
		{"mdia", true},
		{"stbl", true},
		{"moof", true},
		{"mdat", false},
		{"ftyp", false},
		{"free", false},
	}
	for _, tt := range tests {
		box := &Box{}
		copy(box.Type[:], tt.boxType)
		if got := box.IsContainer(); got != tt.want {
			t.Errorf("IsContainer(%s) = %v, want %v", tt.boxType, got, tt.want)
		}
	}
}

func TestBoxHeaderSize(t *testing.T) {
	box := &Box{Size: 100}
	copy(box.Type[:], "mdat")
	if box.HeaderSize() != 8 {
		t.Errorf("HeaderSize() = %d, want 8", box.HeaderSize())
	}

	box.Size = 1 // extended
	if box.HeaderSize() != 16 {
		t.Errorf("HeaderSize() = %d, want 16", box.HeaderSize())
	}
}

func TestReadChildBoxes(t *testing.T) {
	// Create a fake moov body with mvhd + trak children
	// mvhd: size=16, type="mvhd", body=8 bytes
	// trak: size=16, type="trak", body=8 bytes
	body := []byte{
		0, 0, 0, 16, 'm', 'v', 'h', 'd', // mvhd header
		0, 0, 0, 0, 0, 0, 0, 0, // mvhd body
		0, 0, 0, 16, 't', 'r', 'a', 'k', // trak header
		0, 0, 0, 0, 0, 0, 0, 0, // trak body
	}

	parser := &Parser{}
	boxes, err := parser.ReadChildBoxes(body)
	if err != nil {
		t.Fatalf("ReadChildBoxes: %v", err)
	}

	if len(boxes) != 2 {
		t.Fatalf("expected 2 boxes, got %d", len(boxes))
	}

	if boxes[0].BoxType() != "mvhd" {
		t.Errorf("boxes[0].Type = %q, want mvhd", boxes[0].BoxType())
	}
	if boxes[1].BoxType() != "trak" {
		t.Errorf("boxes[1].Type = %q, want trak", boxes[1].BoxType())
	}
}

func TestParseInitSegmentMinimal(t *testing.T) {
	// Build a minimal fMP4 init segment with ftyp + moov
	// This is a simplified test - real init segments are more complex
	data := buildMinimalInit()

	info, err := ParseInitSegment(data)
	if err != nil {
		t.Fatalf("ParseInitSegment: %v", err)
	}

	if info == nil {
		t.Fatal("info is nil")
	}
	// We just verify it doesn't crash; a real init segment would have more data
}

func buildMinimalInit() []byte {
	// ftyp box
	ftyp := []byte{
		0, 0, 0, 0x14, // size = 20
		'f', 't', 'y', 'p', // type
		'i', 's', 'o', 'm', // major brand
		0, 0, 0, 0, // minor version
		'i', 's', 'o', 'm', // compatible brand
	}

	// moov box (minimal)
	moovBody := []byte{
		// mvhd v0: 8(header) + 4(ver+flags) + 4+4+4+4+4+8+8+2+2+2+2+36 = 108
		0, 0, 0, 0x6c, 'm', 'v', 'h', 'd',
		0, 0, 0, 0, // version + flags
		0, 0, 0, 0, // creation time
		0, 0, 0, 0, // modification time
		0, 0, 0x03, 0xe8, // timescale = 1000
		0, 0, 0, 0, // duration
		0, 0, 0, 0, 0, 0, 0, 0, // rate + volume
		0, 0, 0, 0, 0, 0, 0, 0, // reserved
		0, 0, 0, 0, 0, 0, 0, 0, // matrix
		0, 0, 0, 0, 0, 0, 0, 0, // matrix continued
		0, 0, 0, 0, 0, 0, 0, 0, // matrix continued
		0, 0, 0, 0, 0, 0, 0, 0, // pre-defined
		0, 0, 0, 0, 0, 0, 0, 0, // pre-defined
		0, 0, 0, 0, 0, 0, 0, 0, // pre-defined
		0, 0, 0, 0, 0, 0, 0, 0, // pre-defined
		0, 0, 0, 0, 0, 0, 0, 0, // pre-defined
		0, 0, 0, 2, // next track id
	}

	moovSize := 8 + len(moovBody)
	moov := make([]byte, moovSize)
	copy(moov[0:4], uint32Bytes(uint32(moovSize)))
	copy(moov[4:8], []byte("moov"))
	copy(moov[8:], moovBody)

	result := make([]byte, len(ftyp)+len(moov))
	copy(result, ftyp)
	copy(result[len(ftyp):], moov)
	return result
}

func uint32Bytes(v uint32) []byte {
	b := make([]byte, 4)
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
	return b
}
