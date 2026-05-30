package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// SubtitleSample represents a single subtitle sample extracted from an MP4 track.
type SubtitleSample struct {
	Index     int     // Sample index (0-based)
	Data      []byte  // Raw subtitle data (XML for TTML, text for WebVTT)
	Duration  float64 // Duration in seconds
	Timestamp float64 // Presentation timestamp in seconds
}

// SubtitleTrackInfo holds metadata about a subtitle track.
type SubtitleTrackInfo struct {
	TrackID     uint32
	Handler     string // "sbtl" or "text"
	Codec       string // "stpp", "wvtt", "text"
	Language    string
	Timescale   uint32
	SampleCount uint32
}

// ExtractSubtitleSamples extracts subtitle samples from an MP4 file.
//
// It parses the moov/trak structure to find subtitle tracks (handler = 'sbtl' or 'text'),
// reads stsd to get the codec format (wvtt/ttml/stpp), and uses stsz + stco to locate
// and extract the raw subtitle data.
//
// If trackID is 0, it returns samples from the first subtitle track found.
// Otherwise, it extracts from the specified trackID.
func ExtractSubtitleSamples(data []byte, trackID uint32) ([]SubtitleSample, error) {
	boxes, err := parseTopLevelBoxes(data)
	if err != nil {
		return nil, err
	}

	// Find moov box
	var moovBody []byte
	for _, box := range boxes {
		if box.BoxType() == "moov" {
			moovBody = box.Body
			break
		}
	}
	if moovBody == nil {
		return nil, errors.New("no moov box found")
	}

	// Parse moov children
	moovBoxes, err := parseBoxesFromBytes(moovBody)
	if err != nil {
		return nil, err
	}

	// Find subtitle trak
	var trakBody []byte
	for _, box := range moovBoxes {
		if box.BoxType() == "trak" {
			info := parseTrakInfo(box.Body)
			if info != nil && isSubtitleHandler(info.Handler) {
				if trackID == 0 || info.TrackID == trackID {
					trakBody = box.Body
					break
				}
			}
		}
	}

	if trakBody == nil {
		return nil, fmt.Errorf("subtitle track not found (trackID=%d)", trackID)
	}

	return extractSamplesFromTrak(trakBody, data)
}

// ListSubtitleTracks returns all subtitle tracks found in the MP4 file.
func ListSubtitleTracks(data []byte) ([]SubtitleTrackInfo, error) {
	boxes, err := parseTopLevelBoxes(data)
	if err != nil {
		return nil, err
	}

	var moovBody []byte
	for _, box := range boxes {
		if box.BoxType() == "moov" {
			moovBody = box.Body
			break
		}
	}
	if moovBody == nil {
		return nil, errors.New("no moov box found")
	}

	moovBoxes, err := parseBoxesFromBytes(moovBody)
	if err != nil {
		return nil, err
	}

	var tracks []SubtitleTrackInfo
	for _, box := range moovBoxes {
		if box.BoxType() == "trak" {
			info := parseTrakInfo(box.Body)
			if info != nil && isSubtitleHandler(info.Handler) {
				tracks = append(tracks, *info)
			}
		}
	}

	return tracks, nil
}

// trakParseInfo is used internally during trak parsing.
type trakParseInfo struct {
	TrackID   uint32
	Handler   string
	Codec     string
	Language  string
	Timescale uint32
}

func isSubtitleHandler(handler string) bool {
	return handler == "sbtl" || handler == "text"
}

// parseTrakInfo extracts track metadata from a trak box body.
func parseTrakInfo(trakBody []byte) *SubtitleTrackInfo {
	boxes, _ := parseBoxesFromBytes(trakBody)
	if boxes == nil {
		return nil
	}

	info := &SubtitleTrackInfo{}

	for _, box := range boxes {
		switch box.BoxType() {
		case "tkhd":
			info.TrackID = parseTkhdTrackID(box.Body)
		case "mdia":
			parseMdiaForSubtitle(box.Body, info)
		}
	}

	return info
}

func parseTkhdTrackID(body []byte) uint32 {
	if len(body) < 20 {
		return 0
	}
	version := body[0]
	// tkhd v0: version(1)+flags(3)+creation(4)+modification(4)+trackID(4) → offset 16
	// tkhd v1: version(1)+flags(3)+creation(8)+modification(8)+trackID(4) → offset 20
	var idOffset int
	if version == 0 {
		idOffset = 16
	} else {
		idOffset = 20
	}
	if len(body) >= idOffset+4 {
		return binary.BigEndian.Uint32(body[idOffset : idOffset+4])
	}
	return 0
}

func parseMdiaForSubtitle(mdiaBody []byte, info *SubtitleTrackInfo) {
	boxes, _ := parseBoxesFromBytes(mdiaBody)
	if boxes == nil {
		return
	}

	for _, box := range boxes {
		switch box.BoxType() {
		case "mdhd":
			info.Timescale = parseMdhdTimescale(box.Body)
			info.Language = parseMdhdLanguage(box.Body)
		case "hdlr":
			info.Handler = parseHdlrHandler(box.Body)
		case "minf":
			parseMinfForSubtitle(box.Body, info)
		}
	}
}

func parseMdhdTimescale(body []byte) uint32 {
	if len(body) < 4 {
		return 0
	}
	version := body[0]
	var offset int
	if version == 0 {
		offset = 4 + 4 + 4
	} else {
		offset = 4 + 8 + 8
	}
	if len(body) >= offset+4 {
		return binary.BigEndian.Uint32(body[offset : offset+4])
	}
	return 0
}

func parseMdhdLanguage(body []byte) string {
	if len(body) < 4 {
		return ""
	}
	version := body[0]
	var langOffset int
	if version == 0 {
		langOffset = 4 + 4 + 4 + 4 // ver+flags + creation + modification + timescale
	} else {
		langOffset = 4 + 8 + 8 + 4
	}
	if len(body) >= langOffset+2 {
		// Language is packed into 2 bytes (ISO-639-2/T)
		lang := binary.BigEndian.Uint16(body[langOffset : langOffset+2])
		c1 := byte((lang >> 10) & 0x1f) + 0x60
		c2 := byte((lang >> 5) & 0x1f) + 0x60
		c3 := byte(lang & 0x1f) + 0x60
		return string([]byte{c1, c2, c3})
	}
	return ""
}

func parseHdlrHandler(body []byte) string {
	// hdlr: version(1) + flags(3) + pre_defined(4) + handler_type(4)
	if len(body) >= 12 {
		return string(body[4:8])
	}
	return ""
}

func parseMinfForSubtitle(minfBody []byte, info *SubtitleTrackInfo) {
	boxes, _ := parseBoxesFromBytes(minfBody)
	if boxes == nil {
		return
	}

	for _, box := range boxes {
		if box.BoxType() == "stbl" {
			parseStblForSubtitle(box.Body, info)
		}
	}
}

func parseStblForSubtitle(stblBody []byte, info *SubtitleTrackInfo) {
	boxes, _ := parseBoxesFromBytes(stblBody)
	if boxes == nil {
		return
	}

	for _, box := range boxes {
		if box.BoxType() == "stsd" {
			info.Codec = parseStsdCodec(box.Body)
		}
	}
}

func parseStsdCodec(body []byte) string {
	if len(body) < 8 {
		return ""
	}
	// stsd: version(1) + flags(3) + entry_count(4) + entries...
	offset := 8
	if offset+8 > len(body) {
		return ""
	}
	// First entry: size(4) + codec(4)
	codec := string(body[offset+4 : offset+8])
	return codec
}

// extractSamplesFromTrak extracts subtitle samples from a trak box.
func extractSamplesFromTrak(trakBody []byte, mp4Data []byte) ([]SubtitleSample, error) {
	boxes, _ := parseBoxesFromBytes(trakBody)
	if boxes == nil {
		return nil, errors.New("failed to parse trak children")
	}

	// Find mdia → minf → stbl
	var stblBody []byte
	for _, box := range boxes {
		if box.BoxType() == "mdia" {
			mdiaBoxes, _ := parseBoxesFromBytes(box.Body)
			for _, mBox := range mdiaBoxes {
				if mBox.BoxType() == "minf" {
					minfBoxes, _ := parseBoxesFromBytes(mBox.Body)
					for _, minfBox := range minfBoxes {
						if minfBox.BoxType() == "stbl" {
							stblBody = minfBox.Body
						}
					}
				}
			}
		}
	}

	if stblBody == nil {
		return nil, errors.New("no stbl found in trak")
	}

	// Parse stbl for stsz, stco, stts, stsc
	stblBoxes, _ := parseBoxesFromBytes(stblBody)
	if stblBoxes == nil {
		return nil, errors.New("failed to parse stbl")
	}

	var sampleSizes []uint32
	var chunkOffsets []uint64
	var sampleToChunk []stscEntry
	var timescale uint32 = 1000
	var timeToSample []sttsEntry

	for _, box := range stblBoxes {
		switch box.BoxType() {
		case "stsz":
			sampleSizes = parseStsz(box.Body)
		case "stco":
			chunkOffsets = parseStco(box.Body)
		case "co64":
			chunkOffsets = parseCo64(box.Body)
		case "stsc":
			sampleToChunk = parseStsc(box.Body)
		case "stts":
			timeToSample = parseStts(box.Body)
		}
	}

	if len(sampleSizes) == 0 || len(chunkOffsets) == 0 {
		return nil, errors.New("missing stsz or stco in subtitle track")
	}

	// Build sample offset table
	sampleOffsets := buildSampleOffsets(sampleSizes, chunkOffsets, sampleToChunk)

	// Build timestamps from stts
	timestamps := buildTimestamps(timeToSample, timescale)

	// Extract samples
	samples := make([]SubtitleSample, len(sampleSizes))
	for i := 0; i < len(sampleSizes); i++ {
		if i >= len(sampleOffsets) {
			break
		}

		offset := sampleOffsets[i]
		size := sampleSizes[i]

		if int(offset)+int(size) > len(mp4Data) {
			return nil, fmt.Errorf("sample %d out of bounds (offset=%d, size=%d, total=%d)",
				i, offset, size, len(mp4Data))
		}

		sample := SubtitleSample{
			Index: i,
			Data:  make([]byte, size),
		}
		copy(sample.Data, mp4Data[offset:offset+uint64(size)])

		if i < len(timestamps) {
			sample.Timestamp = timestamps[i].timestamp
			sample.Duration = timestamps[i].duration
		}

		samples[i] = sample
	}

	return samples, nil
}

// stscEntry represents an stsc (Sample-to-Chunk) entry.
type stscEntry struct {
	FirstChunk      uint32
	SamplesPerChunk uint32
	SampleDescIndex uint32
}

// sttsEntry represents an stts (Time-to-Sample) entry.
type sttsEntry struct {
	SampleCount uint32
	SampleDelta uint32
}

// parseStsz parses the stsz (Sample Size) box.
// stsz: version(1) + flags(3) + sample_size(4) + sample_count(4) + [entry_size(4)]*count
func parseStsz(body []byte) []uint32 {
	if len(body) < 12 {
		return nil
	}
	// version(1) + flags(3)
	defaultSize := binary.BigEndian.Uint32(body[4:8])
	count := binary.BigEndian.Uint32(body[8:12])

	if defaultSize != 0 {
		// All samples have the same size
		sizes := make([]uint32, count)
		for i := range sizes {
			sizes[i] = defaultSize
		}
		return sizes
	}

	sizes := make([]uint32, count)
	offset := 12
	for i := uint32(0); i < count; i++ {
		if offset+4 > len(body) {
			break
		}
		sizes[i] = binary.BigEndian.Uint32(body[offset : offset+4])
		offset += 4
	}
	return sizes
}

// parseStco parses the stco (Chunk Offset) box.
// stco: version(1) + flags(3) + entry_count(4) + [chunk_offset(4)]*count
func parseStco(body []byte) []uint64 {
	if len(body) < 8 {
		return nil
	}
	count := binary.BigEndian.Uint32(body[4:8])
	offsets := make([]uint64, count)
	offset := 8
	for i := uint32(0); i < count; i++ {
		if offset+4 > len(body) {
			break
		}
		offsets[i] = uint64(binary.BigEndian.Uint32(body[offset : offset+4]))
		offset += 4
	}
	return offsets
}

// parseCo64 parses the co64 (64-bit Chunk Offset) box.
func parseCo64(body []byte) []uint64 {
	if len(body) < 8 {
		return nil
	}
	count := binary.BigEndian.Uint32(body[4:8])
	offsets := make([]uint64, count)
	offset := 8
	for i := uint32(0); i < count; i++ {
		if offset+8 > len(body) {
			break
		}
		offsets[i] = binary.BigEndian.Uint64(body[offset : offset+8])
		offset += 8
	}
	return offsets
}

// parseStsc parses the stsc (Sample-to-Chunk) box.
// stsc: version(1) + flags(3) + entry_count(4) + [first_chunk(4) + samples_per_chunk(4) + sample_desc_index(4)]*count
func parseStsc(body []byte) []stscEntry {
	if len(body) < 8 {
		return nil
	}
	count := binary.BigEndian.Uint32(body[4:8])
	entries := make([]stscEntry, count)
	offset := 8
	for i := uint32(0); i < count; i++ {
		if offset+12 > len(body) {
			break
		}
		entries[i].FirstChunk = binary.BigEndian.Uint32(body[offset : offset+4])
		entries[i].SamplesPerChunk = binary.BigEndian.Uint32(body[offset+4 : offset+8])
		entries[i].SampleDescIndex = binary.BigEndian.Uint32(body[offset+8 : offset+12])
		offset += 12
	}
	return entries
}

// parseStts parses the stts (Time-to-Sample) box.
// stts: version(1) + flags(3) + entry_count(4) + [sample_count(4) + sample_delta(4)]*count
func parseStts(body []byte) []sttsEntry {
	if len(body) < 8 {
		return nil
	}
	count := binary.BigEndian.Uint32(body[4:8])
	entries := make([]sttsEntry, count)
	offset := 8
	for i := uint32(0); i < count; i++ {
		if offset+8 > len(body) {
			break
		}
		entries[i].SampleCount = binary.BigEndian.Uint32(body[offset : offset+4])
		entries[i].SampleDelta = binary.BigEndian.Uint32(body[offset+4 : offset+8])
		offset += 8
	}
	return entries
}

// buildSampleOffsets computes the file offset for each sample.
func buildSampleOffsets(sampleSizes []uint32, chunkOffsets []uint64, stscEntries []stscEntry) []uint64 {
	if len(sampleSizes) == 0 || len(chunkOffsets) == 0 || len(stscEntries) == 0 {
		return nil
	}

	// Build a map: chunk index → samples per chunk
	chunkToSamples := make(map[uint32]uint32)
	for i, entry := range stscEntries {
		startChunk := entry.FirstChunk - 1 // 0-based
		var endChunk uint32
		if i+1 < len(stscEntries) {
			endChunk = stscEntries[i+1].FirstChunk - 1
		} else {
			endChunk = uint32(len(chunkOffsets))
		}
		for c := startChunk; c < endChunk; c++ {
			chunkToSamples[c] = entry.SamplesPerChunk
		}
	}

	// Walk through chunks and assign offsets to each sample
	sampleOffsets := make([]uint64, 0, len(sampleSizes))
	sampleIdx := 0

	for chunkIdx := uint32(0); chunkIdx < uint32(len(chunkOffsets)); chunkIdx++ {
		chunkOffset := chunkOffsets[chunkIdx]
		samplesInChunk := chunkToSamples[chunkIdx]
		if samplesInChunk == 0 {
			continue
		}

		currentOffset := chunkOffset
		for s := uint32(0); s < samplesInChunk && sampleIdx < len(sampleSizes); s++ {
			sampleOffsets = append(sampleOffsets, currentOffset)
			currentOffset += uint64(sampleSizes[sampleIdx])
			sampleIdx++
		}
	}

	return sampleOffsets
}

type timestampEntry struct {
	timestamp float64
	duration  float64
}

// buildTimestamps computes presentation timestamps from stts entries.
func buildTimestamps(entries []sttsEntry, timescale uint32) []timestampEntry {
	if len(entries) == 0 || timescale == 0 {
		return nil
	}

	// Count total samples
	totalSamples := uint32(0)
	for _, e := range entries {
		totalSamples += e.SampleCount
	}

	timestamps := make([]timestampEntry, totalSamples)
	currentTime := uint64(0)
	idx := 0

	for _, entry := range entries {
		for i := uint32(0); i < entry.SampleCount && idx < len(timestamps); i++ {
			timestamps[idx].timestamp = float64(currentTime) / float64(timescale)
			timestamps[idx].duration = float64(entry.SampleDelta) / float64(timescale)
			currentTime += uint64(entry.SampleDelta)
			idx++
		}
	}

	return timestamps
}
