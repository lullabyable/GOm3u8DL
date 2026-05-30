package merge

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// TS2MP4Remux converts MPEG-TS files to a fragmented MP4 (fMP4) output.
// It parses PAT/PMT, extracts PES packets with PTS/DTS timestamps,
// and builds ftyp + moov + [moof + mdat] structure.
func TS2MP4Remux(tsFiles []string, outputPath string, opts ...RemuxOption) error {
	if len(tsFiles) == 0 {
		return fmt.Errorf("no TS files to remux")
	}

	cfg := &remuxConfig{
		videoCodec: "h264",
		audioCodec: "aac",
		timescale:  90000, // MPEG-TS default clock
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Collect all samples from all TS files
	var videoSamples []mp4Sample
	var audioSamples []mp4Sample
	var videoTrack *trackInfo
	var audioTrack *trackInfo

	for _, path := range tsFiles {
		vSamples, aSamples, vt, at, err := parseTSFile(path, cfg)
		if err != nil {
			return fmt.Errorf("parse TS %s: %w", path, err)
		}
		videoSamples = append(videoSamples, vSamples...)
		audioSamples = append(audioSamples, aSamples...)
		if vt != nil {
			videoTrack = vt
		}
		if at != nil {
			audioTrack = at
		}
	}

	if len(videoSamples) == 0 && len(audioSamples) == 0 {
		return fmt.Errorf("no media samples found in TS files")
	}

	// Build fMP4 output
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// Write ftyp
	if err := writeFtypBox(out); err != nil {
		return err
	}

	// Write moov
	if err := writeMoovBox(out, cfg, videoTrack, audioTrack); err != nil {
		return err
	}

	// Write moof + mdat for each chunk of samples
	seqNum := uint32(1)
	if len(videoSamples) > 0 {
		for i := 0; i < len(videoSamples); i++ {
			sample := videoSamples[i]
			if err := writeMoofMdat(out, cfg, 1, seqNum, sample, true); err != nil {
				return err
			}
			seqNum++
		}
	}

	if len(audioSamples) > 0 {
		for i := 0; i < len(audioSamples); i++ {
			sample := audioSamples[i]
			if err := writeMoofMdat(out, cfg, 2, seqNum, sample, false); err != nil {
				return err
			}
			seqNum++
		}
	}

	return nil
}

// RemuxOption configures TS2MP4Remux behavior.
type RemuxOption func(*remuxConfig)

type remuxConfig struct {
	videoCodec string // "h264" or "h265"
	audioCodec string // "aac" or "mp3"
	timescale  uint32
}

// WithRemuxVideoCodec sets the video codec hint ("h264", "h265").
func WithRemuxVideoCodec(codec string) RemuxOption {
	return func(c *remuxConfig) { c.videoCodec = codec }
}

// WithRemuxAudioCodec sets the audio codec hint ("aac", "mp3").
func WithRemuxAudioCodec(codec string) RemuxOption {
	return func(c *remuxConfig) { c.audioCodec = codec }
}

// WithRemuxTimescale sets the MP4 timescale (default 90000).
func WithRemuxTimescale(ts uint32) RemuxOption {
	return func(c *remuxConfig) { c.timescale = ts }
}

// --- TS Parsing ---

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	patPID       = 0
)

type trackInfo struct {
	pid        uint16
	streamType uint8
	width      uint16
	height     uint16
}

type mp4Sample struct {
	data       []byte
	pts        int64
	dts        int64
	duration   uint32
	isKeyFrame bool
}

type tsPacket struct {
	pid              uint16
	payloadUnitStart bool
	adaptationCtrl   uint8
	payload          []byte
}

type pesBuffer struct {
	data     []byte
	started  bool
	complete bool
}

func parseTSFile(path string, cfg *remuxConfig) ([]mp4Sample, []mp4Sample, *trackInfo, *trackInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Find sync byte
	startOffset := 0
	for startOffset < len(data) {
		if data[startOffset] == tsSyncByte {
			break
		}
		startOffset++
	}

	if startOffset >= len(data) {
		return nil, nil, nil, nil, fmt.Errorf("no sync byte found in %s", path)
	}

	// Parse PAT/PMT to discover streams
	videoPID := uint16(0)
	audioPID := uint16(0)
	pmtPID := uint16(0)
	videoStreamType := uint8(0)
	audioStreamType := uint8(0)
	patParsed := false
	pmtParsed := false

	// Buffer for PES data per PID
	pesBuffers := make(map[uint16]*pesBuffer)

	// First pass: parse PAT and PMT
	offset := startOffset
	for offset+tsPacketSize <= len(data) {
		pkt, err := parseTSPacket(data[offset : offset+tsPacketSize])
		if err != nil {
			offset += tsPacketSize
			continue
		}

		if pkt.pid == patPID && !patParsed {
			pmtPID = parsePAT(pkt.payload)
			patParsed = true
		} else if pkt.pid == pmtPID && patParsed && !pmtParsed {
			videoPID, audioPID, videoStreamType, audioStreamType = parsePMT(pkt.payload)
			pmtParsed = true
			if videoPID > 0 {
				pesBuffers[videoPID] = &pesBuffer{}
			}
			if audioPID > 0 {
				pesBuffers[audioPID] = &pesBuffer{}
			}
		}

		if patParsed && pmtParsed {
			break
		}

		offset += tsPacketSize
	}

	if !patParsed || !pmtParsed {
		// Fallback: try common PIDs
		videoPID = 256
		audioPID = 257
		videoStreamType = 27 // H.264
		audioStreamType = 15 // AAC
		pesBuffers[videoPID] = &pesBuffer{}
		pesBuffers[audioPID] = &pesBuffer{}
	}

	// Second pass: collect PES packets
	offset = startOffset
	for offset+tsPacketSize <= len(data) {
		pkt, err := parseTSPacket(data[offset : offset+tsPacketSize])
		if err != nil {
			offset += tsPacketSize
			continue
		}

		buf, ok := pesBuffers[pkt.pid]
		if !ok {
			offset += tsPacketSize
			continue
		}

		if pkt.payloadUnitStart && len(buf.data) > 0 {
			buf.complete = true
		}

		if pkt.payloadUnitStart {
			buf.data = pkt.payload
			buf.started = true
		} else if buf.started {
			buf.data = append(buf.data, pkt.payload...)
		}

		offset += tsPacketSize
	}

	// Flush remaining PES buffers
	for _, buf := range pesBuffers {
		if buf.started && len(buf.data) > 0 {
			buf.complete = true
		}
	}

	// Extract samples from PES buffers
	var videoSamples []mp4Sample
	var audioSamples []mp4Sample

	if buf, ok := pesBuffers[videoPID]; ok && buf.complete {
		videoSamples = extractSamplesFromPES(buf.data, true, cfg.timescale)
	}
	if buf, ok := pesBuffers[audioPID]; ok && buf.complete {
		audioSamples = extractSamplesFromPES(buf.data, false, cfg.timescale)
	}

	videoTrack := &trackInfo{
		pid:        videoPID,
		streamType: videoStreamType,
		width:      1920,
		height:     1080,
	}
	audioTrack := &trackInfo{
		pid:        audioPID,
		streamType: audioStreamType,
	}

	return videoSamples, audioSamples, videoTrack, audioTrack, nil
}

func parseTSPacket(data []byte) (*tsPacket, error) {
	if len(data) < tsPacketSize {
		return nil, fmt.Errorf("TS packet too short: %d", len(data))
	}
	if data[0] != tsSyncByte {
		return nil, fmt.Errorf("missing sync byte: 0x%02x", data[0])
	}

	pkt := &tsPacket{}
	pkt.pid = binary.BigEndian.Uint16(data[1:3]) & 0x1fff
	adaptField := (data[3] >> 4) & 0x03
	pkt.adaptationCtrl = adaptField

	payloadOffset := 4

	if adaptField == 2 {
		return pkt, nil // adaptation only, no payload
	}
	if adaptField == 3 {
		adaptLen := int(data[4])
		payloadOffset = 5 + adaptLen
		if payloadOffset > tsPacketSize {
			return pkt, nil
		}
	}

	pkt.payloadUnitStart = (data[1] & 0x40) != 0
	if payloadOffset < len(data) {
		pkt.payload = data[payloadOffset:]
	}

	return pkt, nil
}

func parsePAT(payload []byte) (pmtPID uint16) {
	if len(payload) < 8 {
		return 0
	}
	offset := int(payload[0]) + 1
	if offset+7 > len(payload) {
		return 0
	}
	if payload[offset] != 0x00 {
		return 0
	}
	sectionLen := int(binary.BigEndian.Uint16(payload[offset+1:offset+3]) & 0x0fff)
	offset += 3
	offset += 5 // skip transport_stream_id + reserved + version + section numbers
	for i := offset; i+4 <= offset+sectionLen-4 && i+4 <= len(payload); i += 4 {
		programNum := binary.BigEndian.Uint16(payload[i : i+2])
		pid := binary.BigEndian.Uint16(payload[i+2:i+4]) & 0x1fff
		if programNum != 0 {
			return pid
		}
	}
	return 0
}

func parsePMT(payload []byte) (videoPID, audioPID uint16, videoST, audioST uint8) {
	if len(payload) < 12 {
		return 0, 0, 0, 0
	}
	offset := int(payload[0]) + 1
	if offset+12 > len(payload) {
		return 0, 0, 0, 0
	}
	if payload[offset] != 0x02 {
		return 0, 0, 0, 0
	}
	sectionLen := int(binary.BigEndian.Uint16(payload[offset+1:offset+3]) & 0x0fff)
	offset += 3
	offset += 2 // PCR_PID
	programInfoLen := int(binary.BigEndian.Uint16(payload[offset:offset+2]) & 0x0fff)
	offset += 2 + programInfoLen

	end := offset + sectionLen - 9
	for i := offset; i+5 <= end && i+5 <= len(payload); {
		streamType := payload[i]
		pid := binary.BigEndian.Uint16(payload[i+1:i+3]) & 0x1fff
		esInfoLen := int(binary.BigEndian.Uint16(payload[i+3:i+5]) & 0x0fff)

		switch streamType {
		case 0x1b: // H.264
			videoPID = pid
			videoST = streamType
		case 0x24: // H.265
			videoPID = pid
			videoST = streamType
		case 0x0f: // AAC (ADTS)
			audioPID = pid
			audioST = streamType
		case 0x03, 0x04: // MP3
			audioPID = pid
			audioST = streamType
		}

		i += 5 + esInfoLen
	}

	return videoPID, audioPID, videoST, audioST
}

func extractSamplesFromPES(data []byte, isVideo bool, timescale uint32) []mp4Sample {
	var samples []mp4Sample

	offset := 0
	for offset < len(data) {
		if offset+3 >= len(data) {
			break
		}
		if data[offset] != 0 || data[offset+1] != 0 || data[offset+2] != 1 {
			offset++
			continue
		}

		streamID := data[offset+3]
		if streamID == 0xbd || streamID == 0xbe || streamID == 0xbf ||
			(streamID >= 0xc0 && streamID <= 0xef) || (streamID >= 0xf0 && streamID <= 0xfe) {
			if offset+6 >= len(data) {
				break
			}
			pesLen := int(binary.BigEndian.Uint16(data[offset+4 : offset+6]))

			headerDataLen := int(data[offset+8])
			ptsOffset := offset + 9

			var pts, dts int64
			if headerDataLen >= 5 && ptsOffset+5 <= len(data) {
				pts = parsePTS(data[ptsOffset:])
				dts = pts
			}
			if headerDataLen >= 10 && ptsOffset+10 <= len(data) {
				dts = parsePTS(data[ptsOffset+5:])
			}

			payloadStart := offset + 9 + headerDataLen
			payloadEnd := offset + 6 + pesLen
			if pesLen == 0 {
				payloadEnd = len(data)
			}
			if payloadEnd > len(data) {
				payloadEnd = len(data)
			}

			if payloadStart < payloadEnd {
				payload := make([]byte, payloadEnd-payloadStart)
				copy(payload, data[payloadStart:payloadEnd])

				sample := mp4Sample{
					data:       payload,
					pts:        pts,
					dts:        dts,
					duration:   3600,
					isKeyFrame: isKeyFrame(payload, isVideo),
				}
				samples = append(samples, sample)
			}

			offset = payloadEnd
		} else {
			offset += 4
		}
	}

	return samples
}

func parsePTS(data []byte) int64 {
	if len(data) < 5 {
		return 0
	}
	pts := int64(data[0]&0x0e) << 29
	pts |= int64(binary.BigEndian.Uint16(data[1:3])) << 14
	pts |= int64(binary.BigEndian.Uint16(data[3:5])) >> 1
	return pts
}

func isKeyFrame(data []byte, isVideo bool) bool {
	if !isVideo {
		return true
	}
	if len(data) < 4 {
		return false
	}
	for i := 0; i < len(data)-4; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			nalStart := i + 2
			if data[nalStart] == 1 {
				nalStart++
			} else if nalStart+1 < len(data) && data[nalStart] == 0 && data[nalStart+1] == 1 {
				nalStart += 2
			} else {
				continue
			}
			if nalStart >= len(data) {
				continue
			}
			nalType := data[nalStart] & 0x1f
			if nalType == 5 || nalType == 7 || nalType == 8 {
				return true
			}
		}
	}
	return true
}

// --- fMP4 Box Building ---

func writeBox(w io.Writer, boxType string, payload []byte) error {
	size := uint32(8 + len(payload))
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], boxType)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func writeFtypBox(w io.Writer) error {
	payload := []byte{
		'i', 's', 'o', 'm',
		0, 0, 0, 0,
		'i', 's', 'o', 'm',
		'i', 's', 'o', '6',
		'm', 'p', '4', '1',
	}
	return writeBox(w, "ftyp", payload)
}

func writeMoovBox(w io.Writer, cfg *remuxConfig, videoTrack, audioTrack *trackInfo) error {
	var moovBody []byte

	mvhd := buildMvhd(cfg.timescale)
	mvhdBox := make([]byte, 8+len(mvhd))
	binary.BigEndian.PutUint32(mvhdBox[0:4], uint32(len(mvhdBox)))
	copy(mvhdBox[4:8], "mvhd")
	copy(mvhdBox[8:], mvhd)
	moovBody = append(moovBody, mvhdBox...)

	if videoTrack != nil {
		trak := buildTrak(cfg, videoTrack, 1, true)
		moovBody = append(moovBody, trak...)
	}

	if audioTrack != nil {
		trak := buildTrak(cfg, audioTrack, 2, false)
		moovBody = append(moovBody, trak...)
	}

	mvex := buildMvex(videoTrack != nil, audioTrack != nil)
	moovBody = append(moovBody, mvex...)

	return writeBox(w, "moov", moovBody)
}

func buildMvhd(timescale uint32) []byte {
	body := make([]byte, 108)
	body[0] = 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	binary.BigEndian.PutUint32(body[16:20], 0x00010000) // rate = 1.0
	body[20] = 0x01
	body[21] = 0x00
	binary.BigEndian.PutUint32(body[104:108], 3) // next track ID
	return body
}

func buildTrak(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool) []byte {
	var trakBody []byte

	tkhd := buildTkhd(trackID, isVideo)
	tkhdBox := make([]byte, 8+len(tkhd))
	binary.BigEndian.PutUint32(tkhdBox[0:4], uint32(len(tkhdBox)))
	copy(tkhdBox[4:8], "tkhd")
	copy(tkhdBox[8:], tkhd)
	trakBody = append(trakBody, tkhdBox...)

	mdia := buildMdia(cfg, track, isVideo)
	trakBody = append(trakBody, mdia...)

	result := make([]byte, 8+len(trakBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "trak")
	copy(result[8:], trakBody)
	return result
}

func buildTkhd(trackID uint32, isVideo bool) []byte {
	body := make([]byte, 84)
	body[0] = 0
	body[1] = 0
	body[2] = 0
	body[3] = 0x03 // track_enabled | track_in_movie | track_in_preview
	binary.BigEndian.PutUint32(body[20:24], trackID)
	if isVideo {
		binary.BigEndian.PutUint32(body[76:80], 1920<<16)
		binary.BigEndian.PutUint32(body[80:84], 1080<<16)
	}
	return body
}

func buildMdia(cfg *remuxConfig, track *trackInfo, isVideo bool) []byte {
	var mdiaBody []byte

	mdhd := buildMdhd(cfg.timescale)
	mdhdBox := make([]byte, 8+len(mdhd))
	binary.BigEndian.PutUint32(mdhdBox[0:4], uint32(len(mdhdBox)))
	copy(mdhdBox[4:8], "mdhd")
	copy(mdhdBox[8:], mdhd)
	mdiaBody = append(mdiaBody, mdhdBox...)

	hdlr := buildHdlr(isVideo)
	hdlrBox := make([]byte, 8+len(hdlr))
	binary.BigEndian.PutUint32(hdlrBox[0:4], uint32(len(hdlrBox)))
	copy(hdlrBox[4:8], "hdlr")
	copy(hdlrBox[8:], hdlr)
	mdiaBody = append(mdiaBody, hdlrBox...)

	minf := buildMinf(cfg, track, isVideo)
	mdiaBody = append(mdiaBody, minf...)

	result := make([]byte, 8+len(mdiaBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "mdia")
	copy(result[8:], mdiaBody)
	return result
}

func buildMdhd(timescale uint32) []byte {
	body := make([]byte, 32)
	body[0] = 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	body[20] = 0x55
	body[21] = 0xc4 // und
	return body
}

func buildHdlr(isVideo bool) []byte {
	body := make([]byte, 33)
	body[0] = 0
	if isVideo {
		copy(body[8:12], "vide")
		copy(body[12:16], "VideoHandler")
	} else {
		copy(body[8:12], "soun")
		copy(body[12:16], "SoundHandler")
	}
	return body
}

func buildMinf(cfg *remuxConfig, track *trackInfo, isVideo bool) []byte {
	var minfBody []byte

	if isVideo {
		vmhd := make([]byte, 12)
		vmhd[0] = 0
		vmhdBox := make([]byte, 20)
		binary.BigEndian.PutUint32(vmhdBox[0:4], 20)
		copy(vmhdBox[4:8], "vmhd")
		copy(vmhdBox[8:], vmhd)
		minfBody = append(minfBody, vmhdBox...)
	} else {
		smhd := make([]byte, 8)
		smhd[0] = 0
		smhdBox := make([]byte, 16)
		binary.BigEndian.PutUint32(smhdBox[0:4], 16)
		copy(smhdBox[4:8], "smhd")
		copy(smhdBox[8:], smhd)
		minfBody = append(minfBody, smhdBox...)
	}

	dref := buildDref()
	drefBox := make([]byte, 8+len(dref))
	binary.BigEndian.PutUint32(drefBox[0:4], uint32(len(drefBox)))
	copy(drefBox[4:8], "dref")
	copy(drefBox[8:], dref)
	dinfBody := drefBox
	dinfBox := make([]byte, 8+len(dinfBody))
	binary.BigEndian.PutUint32(dinfBox[0:4], uint32(len(dinfBox)))
	copy(dinfBox[4:8], "dinf")
	copy(dinfBox[8:], dinfBody)
	minfBody = append(minfBody, dinfBox...)

	stbl := buildStbl(cfg, track, isVideo)
	minfBody = append(minfBody, stbl...)

	result := make([]byte, 8+len(minfBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "minf")
	copy(result[8:], minfBody)
	return result
}

func buildDref() []byte {
	urlEntry := make([]byte, 12)
	binary.BigEndian.PutUint32(urlEntry[0:4], 12)
	copy(urlEntry[4:8], "url ")
	urlEntry[11] = 0x01

	body := make([]byte, 8+len(urlEntry))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1)
	copy(body[8:], urlEntry)
	return body
}

func buildStbl(cfg *remuxConfig, track *trackInfo, isVideo bool) []byte {
	var stblBody []byte

	stsd := buildStsd(cfg, track, isVideo)
	stsdBox := make([]byte, 8+len(stsd))
	binary.BigEndian.PutUint32(stsdBox[0:4], uint32(len(stsdBox)))
	copy(stsdBox[4:8], "stsd")
	copy(stsdBox[8:], stsd)
	stblBody = append(stblBody, stsdBox...)

	for _, name := range []string{"stts", "stsc", "stsz", "stco"} {
		box := make([]byte, 16)
		binary.BigEndian.PutUint32(box[0:4], 16)
		copy(box[4:8], name)
		stblBody = append(stblBody, box...)
	}

	result := make([]byte, 8+len(stblBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "stbl")
	copy(result[8:], stblBody)
	return result
}

func buildStsd(cfg *remuxConfig, track *trackInfo, isVideo bool) []byte {
	var entry []byte

	if isVideo {
		codec := "avc1"
		if cfg.videoCodec == "h265" {
			codec = "hev1"
		}
		entry = make([]byte, 86)
		binary.BigEndian.PutUint32(entry[0:4], 86)
		copy(entry[4:8], codec)
		binary.BigEndian.PutUint16(entry[10:12], 1)
		binary.BigEndian.PutUint16(entry[32:34], track.width)
		binary.BigEndian.PutUint16(entry[34:36], track.height)
		binary.BigEndian.PutUint32(entry[36:40], 0x00480000)
		binary.BigEndian.PutUint32(entry[40:44], 0x00480000)
		binary.BigEndian.PutUint16(entry[62:64], 1)
	} else {
		codec := "mp4a"
		entry = make([]byte, 44)
		binary.BigEndian.PutUint32(entry[0:4], 44)
		copy(entry[4:8], codec)
		binary.BigEndian.PutUint16(entry[10:12], 1)
		binary.BigEndian.PutUint16(entry[16:18], 2)       // channels
		binary.BigEndian.PutUint16(entry[18:20], 16)      // sample size
		binary.BigEndian.PutUint32(entry[24:28], 0xac440000) // 44100 Hz
	}

	body := make([]byte, 8+len(entry))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1)
	copy(body[8:], entry)
	return body
}

func buildMvex(hasVideo, hasAudio bool) []byte {
	var body []byte
	trexID := uint32(1)
	if hasVideo {
		trex := buildTrex(trexID)
		body = append(body, trex...)
		trexID++
	}
	if hasAudio {
		trex := buildTrex(trexID)
		body = append(body, trex...)
	}

	result := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "mvex")
	copy(result[8:], body)
	return result
}

func buildTrex(trackID uint32) []byte {
	inner := make([]byte, 24)
	inner[0] = 0
	binary.BigEndian.PutUint32(inner[4:8], trackID)
	binary.BigEndian.PutUint32(inner[8:12], 1)

	trex := make([]byte, 32)
	binary.BigEndian.PutUint32(trex[0:4], 32)
	copy(trex[4:8], "trex")
	copy(trex[8:], inner)
	return trex
}

func writeMoofMdat(w io.Writer, cfg *remuxConfig, trackID, seqNum uint32, sample mp4Sample, isVideo bool) error {
	moofBody := buildMoof(cfg, trackID, seqNum, sample, isVideo)
	moofBox := make([]byte, 8+len(moofBody))
	binary.BigEndian.PutUint32(moofBox[0:4], uint32(len(moofBox)))
	copy(moofBox[4:8], "moof")
	copy(moofBox[8:], moofBody)

	if _, err := w.Write(moofBox); err != nil {
		return err
	}

	return writeBox(w, "mdat", sample.data)
}

func buildMoof(cfg *remuxConfig, trackID, seqNum uint32, sample mp4Sample, isVideo bool) []byte {
	var moofBody []byte

	mfhd := buildMfhd(seqNum)
	mfhdBox := make([]byte, 8+len(mfhd))
	binary.BigEndian.PutUint32(mfhdBox[0:4], uint32(len(mfhdBox)))
	copy(mfhdBox[4:8], "mfhd")
	copy(mfhdBox[8:], mfhd)
	moofBody = append(moofBody, mfhdBox...)

	traf := buildTraf(cfg, trackID, sample, isVideo)
	moofBody = append(moofBody, traf...)

	return moofBody
}

func buildMfhd(seqNum uint32) []byte {
	body := make([]byte, 8)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], seqNum)
	return body
}

func buildTraf(cfg *remuxConfig, trackID uint32, sample mp4Sample, isVideo bool) []byte {
	var trafBody []byte

	tfhd := buildTfhd(trackID)
	tfhdBox := make([]byte, 8+len(tfhd))
	binary.BigEndian.PutUint32(tfhdBox[0:4], uint32(len(tfhdBox)))
	copy(tfhdBox[4:8], "tfhd")
	copy(tfhdBox[8:], tfhd)
	trafBody = append(trafBody, tfhdBox...)

	tfdt := buildTfdt(uint64(sample.dts))
	tfdtBox := make([]byte, 8+len(tfdt))
	binary.BigEndian.PutUint32(tfdtBox[0:4], uint32(len(tfdtBox)))
	copy(tfdtBox[4:8], "tfdt")
	copy(tfdtBox[8:], tfdt)
	trafBody = append(trafBody, tfdtBox...)

	trun := buildTrun(sample, isVideo)
	trunBox := make([]byte, 8+len(trun))
	binary.BigEndian.PutUint32(trunBox[0:4], uint32(len(trunBox)))
	copy(trunBox[4:8], "trun")
	copy(trunBox[8:], trun)
	trafBody = append(trafBody, trunBox...)

	result := make([]byte, 8+len(trafBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "traf")
	copy(result[8:], trafBody)
	return result
}

func buildTfhd(trackID uint32) []byte {
	body := make([]byte, 12)
	body[0] = 0
	body[1] = 0x02 // default-sample-duration-present
	binary.BigEndian.PutUint32(body[4:8], trackID)
	binary.BigEndian.PutUint32(body[8:12], 3600)
	return body
}

func buildTfdt(baseMediaDecodeTime uint64) []byte {
	body := make([]byte, 12)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:12], uint32(baseMediaDecodeTime))
	return body
}

func buildTrun(sample mp4Sample, isVideo bool) []byte {
	flags := uint32(0x000001) // data-offset-present
	if isVideo && sample.isKeyFrame {
		flags |= 0x000200
	}

	body := make([]byte, 20)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], flags)
	binary.BigEndian.PutUint32(body[8:12], 1) // sample count
	binary.BigEndian.PutUint32(body[12:16], 0) // data_offset placeholder
	binary.BigEndian.PutUint32(body[16:20], uint32(len(sample.data)))
	return body
}
