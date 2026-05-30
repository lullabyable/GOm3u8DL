package mp4

import (
	"encoding/binary"
	"testing"
)

// --- SubtitleSample struct test ---

func TestSubtitleSampleFields(t *testing.T) {
	s := SubtitleSample{
		Index:     0,
		Data:      []byte("Hello"),
		Duration:  2.5,
		Timestamp: 1.0,
	}
	if s.Index != 0 {
		t.Errorf("Index = %d, want 0", s.Index)
	}
	if string(s.Data) != "Hello" {
		t.Errorf("Data = %q, want Hello", s.Data)
	}
	if s.Duration != 2.5 {
		t.Errorf("Duration = %f, want 2.5", s.Duration)
	}
	if s.Timestamp != 1.0 {
		t.Errorf("Timestamp = %f, want 1.0", s.Timestamp)
	}
}

// --- ParseStsz tests ---

func TestParseStsz_AllSameSize(t *testing.T) {
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[4:8], 100) // default size
	binary.BigEndian.PutUint32(body[8:12], 5)  // sample count

	sizes := parseStsz(body)
	if len(sizes) != 5 {
		t.Fatalf("sizes len = %d, want 5", len(sizes))
	}
	for i, s := range sizes {
		if s != 100 {
			t.Errorf("sizes[%d] = %d, want 100", i, s)
		}
	}
}

func TestParseStsz_IndividualSizes(t *testing.T) {
	// 3 samples with individual sizes: 10, 20, 30
	body := make([]byte, 12+3*4)
	binary.BigEndian.PutUint32(body[4:8], 0)  // default size = 0 (individual)
	binary.BigEndian.PutUint32(body[8:12], 3) // count
	binary.BigEndian.PutUint32(body[12:16], 10)
	binary.BigEndian.PutUint32(body[16:20], 20)
	binary.BigEndian.PutUint32(body[20:24], 30)

	sizes := parseStsz(body)
	if len(sizes) != 3 {
		t.Fatalf("sizes len = %d, want 3", len(sizes))
	}
	if sizes[0] != 10 || sizes[1] != 20 || sizes[2] != 30 {
		t.Errorf("sizes = %v, want [10, 20, 30]", sizes)
	}
}

func TestParseStsz_TooShort(t *testing.T) {
	sizes := parseStsz(make([]byte, 4))
	if sizes != nil {
		t.Errorf("expected nil for too-short body")
	}
}

// --- ParseStco tests ---

func TestParseStco(t *testing.T) {
	body := make([]byte, 8+3*4)
	binary.BigEndian.PutUint32(body[4:8], 3) // count
	binary.BigEndian.PutUint32(body[8:12], 1000)
	binary.BigEndian.PutUint32(body[12:16], 2000)
	binary.BigEndian.PutUint32(body[16:20], 3000)

	offsets := parseStco(body)
	if len(offsets) != 3 {
		t.Fatalf("offsets len = %d, want 3", len(offsets))
	}
	if offsets[0] != 1000 || offsets[1] != 2000 || offsets[2] != 3000 {
		t.Errorf("offsets = %v, want [1000, 2000, 3000]", offsets)
	}
}

func TestParseStco_TooShort(t *testing.T) {
	offsets := parseStco(make([]byte, 4))
	if offsets != nil {
		t.Error("expected nil for too-short body")
	}
}

// --- ParseCo64 tests ---

func TestParseCo64(t *testing.T) {
	body := make([]byte, 8+2*8)
	binary.BigEndian.PutUint32(body[4:8], 2) // count
	binary.BigEndian.PutUint64(body[8:16], 0x100000000)  // > 4GB
	binary.BigEndian.PutUint64(body[16:24], 0x200000000)

	offsets := parseCo64(body)
	if len(offsets) != 2 {
		t.Fatalf("offsets len = %d, want 2", len(offsets))
	}
	if offsets[0] != 0x100000000 {
		t.Errorf("offsets[0] = 0x%x, want 0x100000000", offsets[0])
	}
}

// --- ParseStsc tests ---

func TestParseStsc(t *testing.T) {
	// 2 entries
	body := make([]byte, 8+2*12)
	binary.BigEndian.PutUint32(body[4:8], 2)
	// Entry 1: first_chunk=1, samples_per_chunk=10, desc_index=1
	binary.BigEndian.PutUint32(body[8:12], 1)
	binary.BigEndian.PutUint32(body[12:16], 10)
	binary.BigEndian.PutUint32(body[16:20], 1)
	// Entry 2: first_chunk=5, samples_per_chunk=5, desc_index=1
	binary.BigEndian.PutUint32(body[20:24], 5)
	binary.BigEndian.PutUint32(body[24:28], 5)
	binary.BigEndian.PutUint32(body[28:32], 1)

	entries := parseStsc(body)
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	if entries[0].FirstChunk != 1 || entries[0].SamplesPerChunk != 10 {
		t.Errorf("entry[0] = {%d, %d}", entries[0].FirstChunk, entries[0].SamplesPerChunk)
	}
	if entries[1].FirstChunk != 5 || entries[1].SamplesPerChunk != 5 {
		t.Errorf("entry[1] = {%d, %d}", entries[1].FirstChunk, entries[1].SamplesPerChunk)
	}
}

// --- ParseStts tests ---

func TestParseStts(t *testing.T) {
	body := make([]byte, 8+2*8)
	binary.BigEndian.PutUint32(body[4:8], 2)
	// Entry 1: 10 samples, delta=1000
	binary.BigEndian.PutUint32(body[8:12], 10)
	binary.BigEndian.PutUint32(body[12:16], 1000)
	// Entry 2: 5 samples, delta=2000
	binary.BigEndian.PutUint32(body[16:20], 5)
	binary.BigEndian.PutUint32(body[20:24], 2000)

	entries := parseStts(body)
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	if entries[0].SampleCount != 10 || entries[0].SampleDelta != 1000 {
		t.Errorf("entry[0] = {%d, %d}", entries[0].SampleCount, entries[0].SampleDelta)
	}
}

// --- BuildTimestamps tests ---

func TestBuildTimestamps(t *testing.T) {
	entries := []sttsEntry{
		{SampleCount: 3, SampleDelta: 1000},
		{SampleCount: 2, SampleDelta: 2000},
	}
	timestamps := buildTimestamps(entries, 1000)
	if len(timestamps) != 5 {
		t.Fatalf("timestamps len = %d, want 5", len(timestamps))
	}

	expected := []struct {
		ts float64
		d  float64
	}{
		{0.0, 1.0},
		{1.0, 1.0},
		{2.0, 1.0},
		{3.0, 2.0},
		{5.0, 2.0},
	}

	for i, exp := range expected {
		if timestamps[i].timestamp != exp.ts {
			t.Errorf("timestamps[%d].timestamp = %f, want %f", i, timestamps[i].timestamp, exp.ts)
		}
		if timestamps[i].duration != exp.d {
			t.Errorf("timestamps[%d].duration = %f, want %f", i, timestamps[i].duration, exp.d)
		}
	}
}

func TestBuildTimestamps_Empty(t *testing.T) {
	ts := buildTimestamps(nil, 1000)
	if ts != nil {
		t.Error("expected nil for empty entries")
	}
}

// --- BuildSampleOffsets tests ---

func TestBuildSampleOffsets_Simple(t *testing.T) {
	// 3 chunks, each with 2 samples of size 100
	sampleSizes := []uint32{100, 100, 100, 100, 100, 100}
	chunkOffsets := []uint64{1000, 1500, 2000}
	stsc := []stscEntry{
		{FirstChunk: 1, SamplesPerChunk: 2, SampleDescIndex: 1},
	}

	offsets := buildSampleOffsets(sampleSizes, chunkOffsets, stsc)
	if len(offsets) != 6 {
		t.Fatalf("offsets len = %d, want 6", len(offsets))
	}

	expected := []uint64{1000, 1100, 1500, 1600, 2000, 2100}
	for i, exp := range expected {
		if offsets[i] != exp {
			t.Errorf("offsets[%d] = %d, want %d", i, offsets[i], exp)
		}
	}
}

func TestBuildSampleOffsets_MultipleStsc(t *testing.T) {
	// Chunk 1-2: 2 samples each, Chunk 3+: 1 sample each
	sampleSizes := []uint32{50, 50, 50, 50, 100, 100}
	chunkOffsets := []uint64{0, 200, 400, 600}
	stsc := []stscEntry{
		{FirstChunk: 1, SamplesPerChunk: 2, SampleDescIndex: 1},
		{FirstChunk: 3, SamplesPerChunk: 1, SampleDescIndex: 1},
	}

	offsets := buildSampleOffsets(sampleSizes, chunkOffsets, stsc)
	if len(offsets) != 6 {
		t.Fatalf("offsets len = %d, want 6", len(offsets))
	}

	expected := []uint64{0, 50, 200, 250, 400, 600}
	for i, exp := range expected {
		if offsets[i] != exp {
			t.Errorf("offsets[%d] = %d, want %d", i, offsets[i], exp)
		}
	}
}

func TestBuildSampleOffsets_Empty(t *testing.T) {
	offsets := buildSampleOffsets(nil, nil, nil)
	if offsets != nil {
		t.Error("expected nil for empty inputs")
	}
}

// --- IsSubtitleHandler tests ---

func TestIsSubtitleHandler(t *testing.T) {
	tests := []struct {
		handler string
		want    bool
	}{
		{"sbtl", true},
		{"text", true},
		{"vide", false},
		{"soun", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSubtitleHandler(tt.handler); got != tt.want {
			t.Errorf("isSubtitleHandler(%q) = %v, want %v", tt.handler, got, tt.want)
		}
	}
}

// --- ParseMdhdLanguage tests ---

func TestParseMdhdLanguage(t *testing.T) {
	// Build mdhd v0 with language "eng" (packed)
	body := make([]byte, 32)
	body[0] = 0 // version 0
	// ver(1)+flags(3)+creation(4)+modification(4)+timescale(4) = 16
	// language at offset 16
	lang := (uint16('e'-0x60) << 10) | (uint16('n'-0x60) << 5) | uint16('g'-0x60)
	binary.BigEndian.PutUint16(body[16:18], lang)

	langStr := parseMdhdLanguage(body)
	if langStr != "eng" {
		t.Errorf("language = %q, want eng", langStr)
	}
}

// --- ParseHdlrHandler tests ---

func TestParseHdlrHandler(t *testing.T) {
	body := make([]byte, 12)
	// version(1) + flags(3) = 4 bytes, then handler_type(4) at offset 4
	copy(body[4:8], []byte("sbtl"))

	handler := parseHdlrHandler(body)
	if handler != "sbtl" {
		t.Errorf("handler = %q, want sbtl", handler)
	}
}

func TestParseHdlrHandler_TooShort(t *testing.T) {
	handler := parseHdlrHandler(make([]byte, 4))
	if handler != "" {
		t.Errorf("handler = %q, want empty", handler)
	}
}

// --- ParseStsdCodec tests ---

func TestParseStsdCodec(t *testing.T) {
	// stsd: version(1) + flags(3) + entry_count(4) + entry_size(4) + codec(4) = 16
	body := make([]byte, 16)
	binary.BigEndian.PutUint32(body[4:8], 1)    // entry count
	binary.BigEndian.PutUint32(body[8:12], 8)   // entry size
	copy(body[12:16], []byte("stpp"))

	codec := parseStsdCodec(body)
	if codec != "stpp" {
		t.Errorf("codec = %q, want stpp", codec)
	}
}

// --- ExtractSubtitleSamples integration test ---

func TestExtractSubtitleSamples_Integration(t *testing.T) {
	// Build a minimal MP4 with a subtitle track
	mp4Data := buildTestSubtitleMP4(t)

	samples, err := ExtractSubtitleSamples(mp4Data, 0)
	if err != nil {
		t.Fatalf("ExtractSubtitleSamples: %v", err)
	}

	if len(samples) != 2 {
		t.Fatalf("samples len = %d, want 2", len(samples))
	}

	if string(samples[0].Data) != "subtitle1" {
		t.Errorf("samples[0].Data = %q, want subtitle1", samples[0].Data)
	}
	if string(samples[1].Data) != "subtitle2" {
		t.Errorf("samples[1].Data = %q, want subtitle2", samples[1].Data)
	}

	// Check timestamps
	if samples[0].Timestamp != 0 {
		t.Errorf("samples[0].Timestamp = %f, want 0", samples[0].Timestamp)
	}
	if samples[0].Duration != 2.0 {
		t.Errorf("samples[0].Duration = %f, want 2.0", samples[0].Duration)
	}
	if samples[1].Timestamp != 2.0 {
		t.Errorf("samples[1].Timestamp = %f, want 2.0", samples[1].Timestamp)
	}
}

func TestExtractSubtitleSamples_NoMoov(t *testing.T) {
	// Just an ftyp box
	data := makeBox("ftyp", []byte("isom"))
	_, err := ExtractSubtitleSamples(data, 0)
	if err == nil {
		t.Error("expected error for no moov")
	}
}

func TestExtractSubtitleSamples_NoSubtitleTrack(t *testing.T) {
	// Build MP4 with video track only
	mp4Data := buildTestVideoOnlyMP4(t)
	_, err := ExtractSubtitleSamples(mp4Data, 0)
	if err == nil {
		t.Error("expected error for no subtitle track")
	}
}

func TestExtractSubtitleSamples_SpecificTrackID(t *testing.T) {
	mp4Data := buildTestSubtitleMP4(t)

	// Try with wrong trackID
	_, err := ExtractSubtitleSamples(mp4Data, 999)
	if err == nil {
		t.Error("expected error for non-existent trackID")
	}
}

// --- ListSubtitleTracks tests ---

func TestListSubtitleTracks(t *testing.T) {
	mp4Data := buildTestSubtitleMP4(t)

	tracks, err := ListSubtitleTracks(mp4Data)
	if err != nil {
		t.Fatalf("ListSubtitleTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks len = %d, want 1", len(tracks))
	}
	if tracks[0].Handler != "sbtl" {
		t.Errorf("handler = %q, want sbtl", tracks[0].Handler)
	}
	if tracks[0].Codec != "stpp" {
		t.Errorf("codec = %q, want stpp", tracks[0].Codec)
	}
	if tracks[0].TrackID != 2 {
		t.Errorf("trackID = %d, want 2", tracks[0].TrackID)
	}
}

func TestListSubtitleTracks_NoMoov(t *testing.T) {
	data := makeBox("ftyp", []byte("isom"))
	_, err := ListSubtitleTracks(data)
	if err == nil {
		t.Error("expected error for no moov")
	}
}

// --- Helper: build test MP4 ---

func buildTestSubtitleMP4(t *testing.T) []byte {
	t.Helper()

	subtitleData1 := []byte("subtitle1")
	subtitleData2 := []byte("subtitle2")

	// mdat contains the subtitle sample data
	mdatBody := append(subtitleData1, subtitleData2...)
	mdatBox := makeBox("mdat", mdatBody)

	// The mdat offset needs to be calculated for stco
	// We'll build the moov first, then calculate offsets

	// Build subtitle trak
	stblBody := buildSubtitleStbl(t, 2, uint32(len(subtitleData1)), uint32(len(subtitleData2)))

	stblBox := makeBox("stbl", stblBody)
	minfBox := makeBox("minf", stblBox)

	// mdhd: timescale=1000, duration=4000
	mdhdBody := make([]byte, 32)
	mdhdBody[0] = 0 // version
	binary.BigEndian.PutUint32(mdhdBody[16:20], 1000) // timescale
	binary.BigEndian.PutUint32(mdhdBody[20:24], 4000) // duration
	lang := (uint16('e'-0x60) << 10) | (uint16('n'-0x60) << 5) | uint16('g'-0x60)
	binary.BigEndian.PutUint16(mdhdBody[24:26], lang)
	mdhdBox := makeBox("mdhd", mdhdBody)

	// hdlr: handler_type = "sbtl"
	hdlrBody := make([]byte, 12)
	copy(hdlrBody[4:8], []byte("sbtl"))
	hdlrBox := makeBox("hdlr", hdlrBody)

	mdiaBody := append(mdhdBox, hdlrBox...)
	mdiaBody = append(mdiaBody, minfBox...)
	mdiaBox := makeBox("mdia", mdiaBody)

	// tkhd: trackID=2
	tkhdBody := make([]byte, 80) // v0 tkhd
	tkhdBody[0] = 0
	binary.BigEndian.PutUint32(tkhdBody[16:20], 2) // track ID at offset 16 for v0
	tkhdBox := makeBox("tkhd", tkhdBody)

	trakBody := append(tkhdBox, mdiaBox...)
	trakBox := makeBox("trak", trakBody)

	// mvhd
	mvhdBody := make([]byte, 108)
	mvhdBody[0] = 0
	binary.BigEndian.PutUint32(mvhdBody[16:20], 1000) // timescale
	mvhdBox := makeBox("mvhd", mvhdBody)

	moovBody := append(mvhdBox, trakBox...)
	moovBox := makeBox("moov", moovBody)

	// ftyp
	ftypBody := []byte("isom" + "\x00\x00\x00\x00" + "isom")
	ftypBox := makeBox("ftyp", ftypBody)

	// Calculate moov size to set stco offsets correctly
	moovSize := len(moovBox)

	// Now we need to fix stco to point to the right mdat offset
	// ftyp + moov + mdat header (8 bytes)
	mdatDataOffset := uint64(len(ftypBox) + moovSize + 8)

	// Rebuild stbl with correct offset
	stblBody = buildSubtitleStblWithOffset(t, 2, []uint32{uint32(len(subtitleData1)), uint32(len(subtitleData2))}, mdatDataOffset)
	stblBox = makeBox("stbl", stblBody)
	minfBox = makeBox("minf", stblBox)

	mdiaBody = append(mdhdBox, hdlrBox...)
	mdiaBody = append(mdiaBody, minfBox...)
	mdiaBox = makeBox("mdia", mdiaBody)

	trakBody = append(tkhdBox, mdiaBox...)
	trakBox = makeBox("trak", trakBody)

	moovBody = append(mvhdBox, trakBox...)
	moovBox = makeBox("moov", moovBody)

	// Assemble final MP4
	result := make([]byte, 0, len(ftypBox)+len(moovBox)+len(mdatBox))
	result = append(result, ftypBox...)
	result = append(result, moovBox...)
	result = append(result, mdatBox...)

	return result
}

func buildSubtitleStbl(t *testing.T, sampleCount uint32, sizes ...uint32) []byte {
	t.Helper()
	return buildSubtitleStblWithOffset(t, sampleCount, sizes, 0)
}

func buildSubtitleStblWithOffset(t *testing.T, sampleCount uint32, sizes []uint32, mdatOffset uint64) []byte {
	t.Helper()

	// stsd: entry_count=1, entry: size(4) + codec(4) = "stpp"
	stsdBody := make([]byte, 8+8)
	binary.BigEndian.PutUint32(stsdBody[4:8], 1) // entry count
	binary.BigEndian.PutUint32(stsdBody[8:12], 8) // entry size
	copy(stsdBody[12:16], []byte("stpp"))
	stsdBox := makeBox("stsd", stsdBody)

	// stsz: individual sizes
	stszBody := make([]byte, 12+len(sizes)*4)
	binary.BigEndian.PutUint32(stszBody[4:8], 0)           // default size (individual)
	binary.BigEndian.PutUint32(stszBody[8:12], sampleCount) // sample count
	for i, s := range sizes {
		binary.BigEndian.PutUint32(stszBody[12+i*4:16+i*4], s)
	}
	stszBox := makeBox("stsz", stszBody)

	// stco: 1 chunk offset pointing to mdat data
	stcoBody := make([]byte, 8+4)
	binary.BigEndian.PutUint32(stcoBody[4:8], 1) // entry count
	binary.BigEndian.PutUint32(stcoBody[8:12], uint32(mdatOffset))
	stcoBox := makeBox("stco", stcoBody)

	// stsc: 1 entry, first_chunk=1, samples_per_chunk=sampleCount
	stscBody := make([]byte, 8+12)
	binary.BigEndian.PutUint32(stscBody[4:8], 1)           // entry count
	binary.BigEndian.PutUint32(stscBody[8:12], 1)           // first chunk
	binary.BigEndian.PutUint32(stscBody[12:16], sampleCount) // samples per chunk
	binary.BigEndian.PutUint32(stscBody[16:20], 1)           // sample desc index
	stscBox := makeBox("stsc", stscBody)

	// stts: 1 entry, all samples have same delta
	sttsBody := make([]byte, 8+8)
	binary.BigEndian.PutUint32(sttsBody[4:8], 1)           // entry count
	binary.BigEndian.PutUint32(sttsBody[8:12], sampleCount) // sample count
	binary.BigEndian.PutUint32(sttsBody[12:16], 2000)       // sample delta (2 sec at 1000 timescale)
	sttsBox := makeBox("stts", sttsBody)

	// Combine all stbl children
	result := make([]byte, 0)
	result = append(result, stsdBox...)
	result = append(result, stszBox...)
	result = append(result, stcoBox...)
	result = append(result, stscBox...)
	result = append(result, sttsBox...)
	return result
}

func buildTestVideoOnlyMP4(t *testing.T) []byte {
	t.Helper()

	// Video track with handler "vide"
	stsdBody := make([]byte, 8+8)
	binary.BigEndian.PutUint32(stsdBody[4:8], 1)
	binary.BigEndian.PutUint32(stsdBody[8:12], 8)
	copy(stsdBody[12:16], []byte("avc1"))
	stsdBox := makeBox("stsd", stsdBody)
	stblBox := makeBox("stbl", stsdBox)
	minfBox := makeBox("minf", stblBox)

	hdlrBody := make([]byte, 12)
	copy(hdlrBody[4:8], []byte("vide"))
	hdlrBox := makeBox("hdlr", hdlrBody)

	mdiaBody := append(hdlrBox, minfBox...)
	mdiaBox := makeBox("mdia", mdiaBody)

	tkhdBody := make([]byte, 80)
	tkhdBody[0] = 0
	binary.BigEndian.PutUint32(tkhdBody[16:20], 1)
	tkhdBox := makeBox("tkhd", tkhdBody)

	trakBody := append(tkhdBox, mdiaBox...)
	trakBox := makeBox("trak", trakBody)

	mvhdBody := make([]byte, 108)
	mvhdBody[0] = 0
	mvhdBox := makeBox("mvhd", mvhdBody)

	moovBody := append(mvhdBox, trakBox...)
	moovBox := makeBox("moov", moovBody)

	ftypBox := makeBox("ftyp", []byte("isom"))

	result := append(ftypBox, moovBox...)
	return result
}
