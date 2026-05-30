package merge

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/lullabyable/GOm3u8DL/pkg/mp4"
)

// MuxFMP4Streams merges separate video and audio fMP4 streams into a single
// fMP4 file. Each stream is expected to be a complete fMP4 (init + media segments).
// This is a pure Go implementation that rewrites moov/moof headers.
func MuxFMP4Streams(videoPath, audioPath, outputPath string) error {
	videoData, err := os.ReadFile(videoPath)
	if err != nil {
		return fmt.Errorf("read video: %w", err)
	}
	audioData, err := os.ReadFile(audioPath)
	if err != nil {
		return fmt.Errorf("read audio: %w", err)
	}

	videoBoxes, err := mp4.ParseInitSegment(videoData)
	if err != nil {
		return fmt.Errorf("parse video init: %w", err)
	}
	audioBoxes, err := mp4.ParseInitSegment(audioData)
	if err != nil {
		return fmt.Errorf("parse audio init: %w", err)
	}

	_ = videoBoxes
	_ = audioBoxes

	// Split each stream into init (ftyp+moov) and media (moof+mdat...) parts
	videoInit, videoMedia := splitInitMedia(videoData)
	audioInit, audioMedia := splitInitMedia(audioData)

	if videoInit == nil {
		return fmt.Errorf("video stream has no init segment")
	}
	if audioInit == nil {
		return fmt.Errorf("audio stream has no init segment")
	}

	// Parse moov from both init segments
	videoMoov := findBox(videoInit, "moov")
	audioMoov := findBox(audioInit, "moov")
	if videoMoov == nil || audioMoov == nil {
		return fmt.Errorf("missing moov box in init segment")
	}

	// Parse ftyp from video init
	videoFtyp := findBox(videoInit, "ftyp")

	// Build combined output
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// 1. Write ftyp (from video)
	if videoFtyp != nil {
		if err := writeRawBox(out, videoFtyp); err != nil {
			return err
		}
	}

	// 2. Build and write combined moov with both tracks
	combinedMoov, err := buildCombinedMoov(videoMoov.Body, audioMoov.Body)
	if err != nil {
		return fmt.Errorf("build combined moov: %w", err)
	}
	if err := writeBoxRaw(out, "moov", combinedMoov); err != nil {
		return err
	}

	// 3. Interleave and write media segments
	// Parse media segments from both streams
	videoSegs := parseMediaSegments(videoMedia)
	audioSegs := parseMediaSegments(audioMedia)

	// Assign track IDs: video=1, audio=2
	// Interleave: write video seg, then audio seg, alternating
	seqNum := uint32(1)
	vIdx, aIdx := 0, 0

	for vIdx < len(videoSegs) || aIdx < len(audioSegs) {
		if vIdx < len(videoSegs) {
			rewritten, err := rewriteMoofTrack(videoSegs[vIdx], 1, seqNum)
			if err != nil {
				// Write as-is
				out.Write(videoSegs[vIdx])
			} else {
				out.Write(rewritten)
			}
			seqNum++
			vIdx++
		}
		if aIdx < len(audioSegs) {
			rewritten, err := rewriteMoofTrack(audioSegs[aIdx], 2, seqNum)
			if err != nil {
				out.Write(audioSegs[aIdx])
			} else {
				out.Write(rewritten)
			}
			seqNum++
			aIdx++
		}
	}

	return nil
}

// MuxFMP4FromSegments merges separate video and audio fMP4 segment lists.
// videoInit/audioInit are paths to init segments, videoSegs/audioSegs are
// paths to media segment files.
func MuxFMP4FromSegments(videoInitPath, audioInitPath string, videoSegs, audioSegs []string, outputPath string) error {
	// Read init segments
	videoInit, err := os.ReadFile(videoInitPath)
	if err != nil {
		return fmt.Errorf("read video init: %w", err)
	}
	audioInit, err := os.ReadFile(audioInitPath)
	if err != nil {
		return fmt.Errorf("read audio init: %w", err)
	}

	videoMoov := findBox(videoInit, "moov")
	audioMoov := findBox(audioInit, "moov")
	if videoMoov == nil || audioMoov == nil {
		return fmt.Errorf("missing moov in init segment")
	}
	videoFtyp := findBox(videoInit, "ftyp")

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// ftyp
	if videoFtyp != nil {
		if err := writeRawBox(out, videoFtyp); err != nil {
			return err
		}
	}

	// Combined moov
	combinedMoov, err := buildCombinedMoov(videoMoov.Body, audioMoov.Body)
	if err != nil {
		return fmt.Errorf("build combined moov: %w", err)
	}
	if err := writeBoxRaw(out, "moov", combinedMoov); err != nil {
		return err
	}

	// Interleave segments
	seqNum := uint32(1)
	vIdx, aIdx := 0, 0

	for vIdx < len(videoSegs) || aIdx < len(audioSegs) {
		if vIdx < len(videoSegs) {
			data, err := os.ReadFile(videoSegs[vIdx])
			if err != nil {
				return fmt.Errorf("read video seg %d: %w", vIdx, err)
			}
			rewritten, err := rewriteMoofTrack(data, 1, seqNum)
			if err != nil {
				out.Write(data)
			} else {
				out.Write(rewritten)
			}
			seqNum++
			vIdx++
		}
		if aIdx < len(audioSegs) {
			data, err := os.ReadFile(audioSegs[aIdx])
			if err != nil {
				return fmt.Errorf("read audio seg %d: %w", aIdx, err)
			}
			rewritten, err := rewriteMoofTrack(data, 2, seqNum)
			if err != nil {
				out.Write(data)
			} else {
				out.Write(rewritten)
			}
			seqNum++
			aIdx++
		}
	}

	return nil
}

// splitInitMedia splits an fMP4 byte stream into init data (ftyp+moov)
// and media data (moof+mdat pairs).
func splitInitMedia(data []byte) (init, media []byte) {
	parser := mp4.NewParser(newBytesReadSeeker(data))
	var moovEnd int64

	for {
		box, err := parser.ReadBox()
		if err != nil {
			break
		}
		end := box.Offset + int64(box.Size)
		if box.BoxType() == "moov" {
			moovEnd = end
		}
		if box.BoxType() == "moof" {
			// First moof marks start of media
			if moovEnd == 0 {
				moovEnd = box.Offset
			}
			return data[:moovEnd], data[moovEnd:]
		}
	}

	if moovEnd > 0 {
		return data[:moovEnd], data[moovEnd:]
	}
	return data, nil
}

// findBox finds a top-level box by type in raw fMP4 data.
func findBox(data []byte, boxType string) *mp4.Box {
	parser := mp4.NewParser(newBytesReadSeeker(data))
	for {
		box, err := parser.ReadBox()
		if err != nil {
			break
		}
		if box.BoxType() == boxType {
			return box
		}
	}
	return nil
}

// parseMediaSegments parses moof+mdat pairs from raw media data.
func parseMediaSegments(data []byte) [][]byte {
	var segs [][]byte
	parser := mp4.NewParser(newBytesReadSeeker(data))

	for {
		box, err := parser.ReadBox()
		if err != nil {
			break
		}
		if box.BoxType() == "moof" {
			// This moof + next mdat = one segment
			start := box.Offset
			end := box.Offset + int64(box.Size)
			// Try to read the following mdat
			nextBox, err := parser.ReadBox()
			if err == nil && nextBox.BoxType() == "mdat" {
				end = nextBox.Offset + int64(nextBox.Size)
			}
			segs = append(segs, data[start:end])
		}
	}

	return segs
}

// buildCombinedMoov builds a moov box body containing tracks from both
// video and audio moov boxes. Track IDs are reassigned: video=1, audio=2.
func buildCombinedMoov(videoMoov, audioMoov []byte) ([]byte, error) {
	videoBoxes, _ := mp4.ParseInitSegment(nil) // dummy call, we'll parse manually
	_ = videoBoxes

	var result []byte

	// Parse mvhd from video moov to get timescale
	videoMoovBoxes := mustParseChildBoxes(videoMoov)
	audioMoovBoxes := mustParseChildBoxes(audioMoov)

	// Use video's mvhd as base, update next_track_id=3
	var mvhdBox []byte
	for _, b := range videoMoovBoxes {
		if b.BoxType() == "mvhd" {
			mvhdBox = makeBox("mvhd", b.Body)
			// Update next_track_id to 3 (at the end of mvhd body)
			if len(b.Body) >= 4 {
				body := make([]byte, len(b.Body))
				copy(body, b.Body)
				// next_track_id is the last 4 bytes for version 0
				if len(body) >= 108 {
					binary.BigEndian.PutUint32(body[104:108], 3)
				}
				mvhdBox = makeBox("mvhd", body)
			}
			break
		}
	}
	if mvhdBox == nil {
		// Fallback: create minimal mvhd
		mvhdBody := make([]byte, 108)
		binary.BigEndian.PutUint32(mvhdBody[12:16], 90000) // timescale
		binary.BigEndian.PutUint32(mvhdBody[104:108], 3)   // next_track_id
		mvhdBox = makeBox("mvhd", mvhdBody)
	}
	result = append(result, mvhdBox...)

	// Video trak (track ID = 1)
	videoTrak := findChildBox(videoMoovBoxes, "trak")
	if videoTrak != nil {
		rewritten := rewriteTrakID(videoTrak.Body, 1)
		result = append(result, makeBox("trak", rewritten)...)
	}

	// Audio trak (track ID = 2)
	audioTrak := findChildBox(audioMoovBoxes, "trak")
	if audioTrak != nil {
		rewritten := rewriteTrakID(audioTrak.Body, 2)
		result = append(result, makeBox("trak", rewritten)...)
	}

	// mvex with trex for both tracks
	mvexBody := buildMvexCombined()
	result = append(result, makeBox("mvex", mvexBody)...)

	return result, nil
}

// rewriteTrakID rewrites the track ID in a trak box's tkhd.
func rewriteTrakID(trakBody []byte, newID uint32) []byte {
	// Make a copy
	result := make([]byte, len(trakBody))
	copy(result, trakBody)

	boxes := mustParseChildBoxes(trakBody)
	for _, b := range boxes {
		if b.BoxType() == "tkhd" {
			// tkhd body: version(1) + flags(3) + ... + track_ID(4)
			// version 0: track_ID at offset 20
			// version 1: track_ID at offset 20 (same)
			if len(b.Body) >= 24 {
				binary.BigEndian.PutUint32(result[b.Offset+8+20:b.Offset+8+24], newID)
			}
		}
	}

	return result
}

// buildMvexCombined builds an mvex box with trex entries for both tracks.
func buildMvexCombined() []byte {
	var body []byte

	// trex for track 1 (video)
	trex1 := make([]byte, 32)
	binary.BigEndian.PutUint32(trex1[0:4], 32) // size
	copy(trex1[4:8], "trex")
	binary.BigEndian.PutUint32(trex1[12:16], 1) // track ID
	binary.BigEndian.PutUint32(trex1[16:20], 1) // default_sample_description_index
	body = append(body, trex1...)

	// trex for track 2 (audio)
	trex2 := make([]byte, 32)
	binary.BigEndian.PutUint32(trex2[0:4], 32)
	copy(trex2[4:8], "trex")
	binary.BigEndian.PutUint32(trex2[12:16], 2)
	binary.BigEndian.PutUint32(trex2[16:20], 1)
	body = append(body, trex2...)

	return body
}

// rewriteMoofTrack rewrites a moof+mdat segment to use the given track ID
// and sequence number.
func rewriteMoofTrack(data []byte, trackID, seqNum uint32) ([]byte, error) {
	if len(data) < 8 {
		return data, fmt.Errorf("segment too small")
	}

	result := make([]byte, len(data))
	copy(result, data)

	// Find moof box
	parser := mp4.NewParser(newBytesReadSeeker(data))
	for {
		box, err := parser.ReadBox()
		if err != nil {
			break
		}
		if box.BoxType() == "moof" {
			rewriteMoofInPlace(result, box.Offset, trackID, seqNum)
		}
	}

	return result, nil
}

// rewriteMoofInPlace rewrites mfhd sequence_number and traf track ID in-place.
func rewriteMoofInPlace(data []byte, moofOffset int64, trackID, seqNum uint32) {
	moovBody := data[moofOffset+8:]
	childBoxes, err := (&mp4.Parser{}).ReadChildBoxes(moovBody)
	if err != nil {
		return
	}

	for _, child := range childBoxes {
		switch child.BoxType() {
		case "mfhd":
			// mfhd: version(1) + flags(3) + sequence_number(4)
			mfhdBodyOffset := moofOffset + 8 + child.Offset + 8
			if int(mfhdBodyOffset)+8 <= len(data) {
				binary.BigEndian.PutUint32(data[mfhdBodyOffset+4:mfhdBodyOffset+8], seqNum)
			}
		case "traf":
			rewriteTrafInPlace(data, moofOffset+8+child.Offset, trackID)
		}
	}
}

// rewriteTrafInPlace rewrites the track ID in tfhd within a traf box.
func rewriteTrafInPlace(data []byte, trafOffset int64, trackID uint32) {
	trafBody := data[trafOffset+8:]
	childBoxes, err := (&mp4.Parser{}).ReadChildBoxes(trafBody)
	if err != nil {
		return
	}

	for _, child := range childBoxes {
		if child.BoxType() == "tfhd" {
			// tfhd: version(1) + flags(3) + track_ID(4)
			tfhdBodyOffset := trafOffset + 8 + child.Offset + 8
			if int(tfhdBodyOffset)+8 <= len(data) {
				binary.BigEndian.PutUint32(data[tfhdBodyOffset+4:tfhdBodyOffset+8], trackID)
			}
		}
	}
}

// --- Helpers ---

func mustParseChildBoxes(body []byte) []*mp4.Box {
	boxes, _ := (&mp4.Parser{}).ReadChildBoxes(body)
	return boxes
}

func findChildBox(boxes []*mp4.Box, boxType string) *mp4.Box {
	for _, b := range boxes {
		if b.BoxType() == boxType {
			return b
		}
	}
	return nil
}

func makeBox(boxType string, body []byte) []byte {
	size := uint32(8 + len(body))
	result := make([]byte, size)
	binary.BigEndian.PutUint32(result[0:4], size)
	copy(result[4:8], boxType)
	copy(result[8:], body)
	return result
}

func writeBoxRaw(w io.Writer, boxType string, body []byte) error {
	size := uint32(8 + len(body))
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], boxType)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}

func writeRawBox(w io.Writer, box *mp4.Box) error {
	// Reconstruct the full box (header + body)
	headerSize := 8
	size := uint32(headerSize + len(box.Body))
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], box.BoxType())
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(box.Body); err != nil {
		return err
	}
	return nil
}


