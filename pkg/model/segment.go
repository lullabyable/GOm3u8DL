package model

// MediaSegment represents a single media segment (TS/fMP4 chunk)
// with optional encryption info and byte range.
type MediaSegment struct {
	Index        int64
	Duration     float64
	URL          string
	StartRange   *int64
	ExpectLength *int64
	EncryptInfo  EncryptInfo
}

// MediaPart represents a group of segments (used in LL-HLS partial segments).
type MediaPart struct {
	MediaSegments []MediaSegment
}
