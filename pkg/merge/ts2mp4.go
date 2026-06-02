package merge

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// TS2MP4Remux converts MPEG-TS files to a non-fragmented MP4 output.
// It parses PAT/PMT, extracts PES packets with PTS/DTS timestamps,
// and builds ftyp + moov (with full sample tables) + mdat.
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

	return writeMP4Output(outputPath, cfg, videoSamples, audioSamples, videoTrack, audioTrack)
}

// MuxSeparateTSStreams merges separate video and audio TS segment streams
// into a single MP4 output.
func MuxSeparateTSStreams(videoSegs, audioSegs []string, outputPath string, opts ...RemuxOption) error {
	if len(videoSegs) == 0 && len(audioSegs) == 0 {
		return fmt.Errorf("no TS segments provided")
	}

	cfg := &remuxConfig{
		videoCodec: "h264",
		audioCodec: "aac",
		timescale:  90000,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	var videoSamples, audioSamples []mp4Sample
	var videoTrack, audioTrack *trackInfo

	for _, path := range videoSegs {
		vSamples, _, vt, _, err := parseTSFile(path, cfg)
		if err != nil {
			return fmt.Errorf("parse video TS %s: %w", path, err)
		}
		videoSamples = append(videoSamples, vSamples...)
		if vt != nil {
			videoTrack = vt
		}
	}

	for _, path := range audioSegs {
		_, aSamples, _, at, err := parseTSFile(path, cfg)
		if err != nil {
			return fmt.Errorf("parse audio TS %s: %w", path, err)
		}
		audioSamples = append(audioSamples, aSamples...)
		if at != nil {
			audioTrack = at
		}
	}

	if len(videoSamples) == 0 && len(audioSamples) == 0 {
		return fmt.Errorf("no media samples found in TS segments")
	}

	return writeMP4Output(outputPath, cfg, videoSamples, audioSamples, videoTrack, audioTrack)
}

// writeMP4Output writes a proper non-fragmented MP4: ftyp + moov + mdat.
func writeMP4Output(outputPath string, cfg *remuxConfig, videoSamples, audioSamples []mp4Sample, videoTrack, audioTrack *trackInfo) error {
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// Calculate total mdat size
	videoDataSize := int64(0)
	for i := range videoSamples {
		videoDataSize += int64(len(videoSamples[i].data))
	}
	audioDataSize := int64(0)
	for i := range audioSamples {
		audioDataSize += int64(len(audioSamples[i].data))
	}
	mdatSize := videoDataSize + audioDataSize

	// Calculate moov box size to compute offsets for stco
	videoTrackID := uint32(0)
	audioTrackID := uint32(0)
	if videoTrack != nil {
		videoTrackID = 1
	}
	if audioTrack != nil {
		audioTrackID = 2
		if videoTrack == nil {
			audioTrackID = 1
		}
	}

	// Build moov payload first to know its size
	moovPayload := buildMoovBox(cfg, videoSamples, audioSamples, videoTrack, audioTrack, videoTrackID, audioTrackID, 0)

	// ftyp(20) + moov(8+len(moovPayload)) + mdat(8 or 16)
	ftypSize := int64(20) // 8 header + 12 payload
	moovSize := int64(8 + len(moovPayload))
mdatHeaderSize := int64(8)
	if mdatSize+8 > 0xFFFFFFFF {
		moovSize = int64(8 + len(moovPayload))
		mdatHeaderSize = 16
	}
	mdatOffset := ftypSize + moovSize

	// Rebuild moov with correct stco offset (mdatOffset + mdatHeaderSize)
	dataStartInMdat := mdatOffset + mdatHeaderSize
	moovPayload = buildMoovBox(cfg, videoSamples, audioSamples, videoTrack, audioTrack, videoTrackID, audioTrackID, dataStartInMdat)

	// Write ftyp
	if err := writeFtypBox(out); err != nil {
		return err
	}

	// Write moov
	if err := writeBox(out, "moov", moovPayload); err != nil {
		return err
	}

	// Write mdat
	if mdatSize+8 > 0xFFFFFFFF {
		// Large box (64-bit size)
		hdr := make([]byte, 16)
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], "mdat")
		binary.BigEndian.PutUint64(hdr[8:16], uint64(mdatSize+16))
		if _, err := out.Write(hdr); err != nil {
			return err
		}
	} else {
		if err := writeBoxHeader(out, "mdat", uint32(mdatSize+8)); err != nil {
			return err
		}
	}

	// Write video data then audio data
	for i := range videoSamples {
		if _, err := out.Write(videoSamples[i].data); err != nil {
			return err
		}
	}
	for i := range audioSamples {
		if _, err := out.Write(audioSamples[i].data); err != nil {
			return err
		}
	}

	return nil
}

func writeBoxHeader(w io.Writer, boxType string, size uint32) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], boxType)
	_, err := w.Write(hdr)
	return err
}

// buildMoovBox builds the entire moov box payload with proper sample tables.
func buildMoovBox(cfg *remuxConfig, videoSamples, audioSamples []mp4Sample, videoTrack, audioTrack *trackInfo, videoTrackID, audioTrackID uint32, mdatDataOffset int64) []byte {
	var moovBody []byte

	// mvhd
	mvhd := buildMvhdFull(cfg.timescale, videoTrackID, audioTrackID)
	mvhdBox := make([]byte, 8+len(mvhd))
	binary.BigEndian.PutUint32(mvhdBox[0:4], uint32(len(mvhdBox)))
	copy(mvhdBox[4:8], "mvhd")
	copy(mvhdBox[8:], mvhd)
	moovBody = append(moovBody, mvhdBox...)

	// video trak
	currentOffset := mdatDataOffset
	if videoTrack != nil && len(videoSamples) > 0 {
		trak := buildTrakFull(cfg, videoTrack, videoTrackID, true, videoSamples, currentOffset)
		moovBody = append(moovBody, trak...)
		currentOffset += int64(sumSampleSizes(videoSamples))
	}

	// audio trak
	if audioTrack != nil && len(audioSamples) > 0 {
		trak := buildTrakFull(cfg, audioTrack, audioTrackID, false, audioSamples, currentOffset)
		moovBody = append(moovBody, trak...)
	}

	return moovBody
}

func sumSampleSizes(samples []mp4Sample) int64 {
	var total int64
	for _, s := range samples {
		total += int64(len(s.data))
	}
	return total
}

func buildMvhdFull(timescale uint32, videoTrackID, audioTrackID uint32) []byte {
	body := make([]byte, 108)
	body[0] = 0 // version 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	binary.BigEndian.PutUint32(body[16:20], 0x00010000) // rate = 1.0
	body[20] = 0x01                                      // volume = 1.0
	body[21] = 0x00
	// next_track_ID = max(trackIDs) + 1
	nextID := videoTrackID
	if audioTrackID > nextID {
		nextID = audioTrackID
	}
	binary.BigEndian.PutUint32(body[104:108], nextID+1)
	return body
}

func buildTrakFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, dataOffset int64) []byte {
	var trakBody []byte

	// tkhd
	tkhd := buildTkhdFull(trackID, isVideo, track, cfg.timescale, samples)
	tkhdBox := make([]byte, 8+len(tkhd))
	binary.BigEndian.PutUint32(tkhdBox[0:4], uint32(len(tkhdBox)))
	copy(tkhdBox[4:8], "tkhd")
	copy(tkhdBox[8:], tkhd)
	trakBody = append(trakBody, tkhdBox...)

	// mdia
	mdia := buildMdiaFull(cfg, track, trackID, isVideo, samples, dataOffset)
	trakBody = append(trakBody, mdia...)

	result := make([]byte, 8+len(trakBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "trak")
	copy(result[8:], trakBody)
	return result
}

func buildTkhdFull(trackID uint32, isVideo bool, track *trackInfo, timescale uint32, samples []mp4Sample) []byte {
	body := make([]byte, 84)
	body[0] = 0
	// flags: track_enabled | track_in_movie | track_in_preview
	body[3] = 0x03
	binary.BigEndian.PutUint32(body[20:24], trackID)

	// duration (in movie timescale)
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		duration := last.pts + int64(last.duration)
		if duration > 0 {
			binary.BigEndian.PutUint32(body[24:28], uint32(duration))
		}
	}

	if isVideo {
		binary.BigEndian.PutUint32(body[76:80], uint32(track.width)<<16)
		binary.BigEndian.PutUint32(body[80:84], uint32(track.height)<<16)
	}
	return body
}

func buildMdiaFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, dataOffset int64) []byte {
	var mdiaBody []byte

	// mdhd
	mdhd := buildMdhdFull(cfg.timescale, samples)
	mdhdBox := make([]byte, 8+len(mdhd))
	binary.BigEndian.PutUint32(mdhdBox[0:4], uint32(len(mdhdBox)))
	copy(mdhdBox[4:8], "mdhd")
	copy(mdhdBox[8:], mdhd)
	mdiaBody = append(mdiaBody, mdhdBox...)

	// hdlr
	hdlr := buildHdlr(isVideo)
	hdlrBox := make([]byte, 8+len(hdlr))
	binary.BigEndian.PutUint32(hdlrBox[0:4], uint32(len(hdlrBox)))
	copy(hdlrBox[4:8], "hdlr")
	copy(hdlrBox[8:], hdlr)
	mdiaBody = append(mdiaBody, hdlrBox...)

	// minf
	minf := buildMinfFull(cfg, track, trackID, isVideo, samples, dataOffset)
	mdiaBody = append(mdiaBody, minf...)

	result := make([]byte, 8+len(mdiaBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "mdia")
	copy(result[8:], mdiaBody)
	return result
}

func buildMdhdFull(timescale uint32, samples []mp4Sample) []byte {
	body := make([]byte, 32)
	body[0] = 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	// duration
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		dur := last.pts + int64(last.duration)
		binary.BigEndian.PutUint32(body[16:20], uint32(dur))
	}
	body[20] = 0x55
	body[21] = 0xc4 // language = "und"
	return body
}

func buildMinfFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, dataOffset int64) []byte {
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

	stbl := buildStblFull(cfg, track, trackID, isVideo, samples, dataOffset)
	minfBody = append(minfBody, stbl...)

	result := make([]byte, 8+len(minfBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "minf")
	copy(result[8:], minfBody)
	return result
}

// buildStblFull builds a proper stbl box with stts, stsc, stsz, stco fully populated.
func buildStblFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, dataOffset int64) []byte {
	var stblBody []byte

	// stsd
	stsd := buildStsd(cfg, track, isVideo)
	stsdBox := make([]byte, 8+len(stsd))
	binary.BigEndian.PutUint32(stsdBox[0:4], uint32(len(stsdBox)))
	copy(stsdBox[4:8], "stsd")
	copy(stsdBox[8:], stsd)
	stblBody = append(stblBody, stsdBox...)

	// stts (sample durations) — run-length encode durations
	stts := buildStts(samples)
	sttsBox := make([]byte, 8+len(stts))
	binary.BigEndian.PutUint32(sttsBox[0:4], uint32(len(sttsBox)))
	copy(sttsBox[4:8], "stts")
	copy(sttsBox[8:], stts)
	stblBody = append(stblBody, sttsBox...)

	// stss (sync sample table) — only for video
	if isVideo {
		stss := buildStss(samples)
		stssBox := make([]byte, 8+len(stss))
		binary.BigEndian.PutUint32(stssBox[0:4], uint32(len(stssBox)))
		copy(stssBox[4:8], "stss")
		copy(stssBox[8:], stss)
		stblBody = append(stblBody, stssBox...)
	}

	// stsc (sample-to-chunk) — all samples in one chunk
	stsc := buildStsc(len(samples))
	stscBox := make([]byte, 8+len(stsc))
	binary.BigEndian.PutUint32(stscBox[0:4], uint32(len(stscBox)))
	copy(stscBox[4:8], "stsc")
	copy(stscBox[8:], stsc)
	stblBody = append(stblBody, stscBox...)

	// stsz (sample sizes)
	stsz := buildStsz(samples)
	stszBox := make([]byte, 8+len(stsz))
	binary.BigEndian.PutUint32(stszBox[0:4], uint32(len(stszBox)))
	copy(stszBox[4:8], "stsz")
	copy(stszBox[8:], stsz)
	stblBody = append(stblBody, stszBox...)

	// stco (chunk offsets)
	stco := buildStco(dataOffset)
	stcoBox := make([]byte, 8+len(stco))
	binary.BigEndian.PutUint32(stcoBox[0:4], uint32(len(stcoBox)))
	copy(stcoBox[4:8], "stco")
	copy(stcoBox[8:], stco)
	stblBody = append(stblBody, stcoBox...)

	result := make([]byte, 8+len(stblBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "stbl")
	copy(result[8:], stblBody)
	return result
}

// buildStts creates the decoding time-to-sample table using run-length encoding.
func buildStts(samples []mp4Sample) []byte {
	type entry struct {
		count    uint32
		duration uint32
	}
	var entries []entry
	for _, s := range samples {
		if len(entries) > 0 && entries[len(entries)-1].duration == s.duration {
			entries[len(entries)-1].count++
		} else {
			entries = append(entries, entry{count: 1, duration: s.duration})
		}
	}

	body := make([]byte, 4+4+8*len(entries))
	body[0] = 0 // version
	binary.BigEndian.PutUint32(body[4:8], uint32(len(entries)))
	for i, e := range entries {
		binary.BigEndian.PutUint32(body[8+i*8:12+i*8], e.count)
		binary.BigEndian.PutUint32(body[12+i*8:16+i*8], e.duration)
	}
	return body
}

// buildStss creates the sync sample (keyframe) table.
func buildStss(samples []mp4Sample) []byte {
	var keyframes []uint32
	for i, s := range samples {
		if s.isKeyFrame {
			keyframes = append(keyframes, uint32(i+1)) // 1-based
		}
	}
	if len(keyframes) == 0 {
		for i := range samples {
			keyframes = append(keyframes, uint32(i+1))
		}
	}

	body := make([]byte, 4+4+4*len(keyframes))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], uint32(len(keyframes)))
	for i, kf := range keyframes {
		binary.BigEndian.PutUint32(body[8+i*4:12+i*4], kf)
	}
	return body
}

// buildStsc creates the sample-to-chunk table (all samples in one chunk).
func buildStsc(sampleCount int) []byte {
	body := make([]byte, 4+4+12)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1)                       // entry count
	binary.BigEndian.PutUint32(body[8:12], 1)                       // first chunk
	binary.BigEndian.PutUint32(body[12:16], uint32(sampleCount))    // samples_per_chunk
	binary.BigEndian.PutUint32(body[16:20], 1)                      // sample description index
	return body
}

// buildStsz creates the sample size table.
func buildStsz(samples []mp4Sample) []byte {
	body := make([]byte, 4+4+4+4*len(samples))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 0) // sample_size = 0 (variable)
	binary.BigEndian.PutUint32(body[8:12], uint32(len(samples)))
	for i, s := range samples {
		binary.BigEndian.PutUint32(body[12+i*4:16+i*4], uint32(len(s.data)))
	}
	return body
}

// buildStco creates the chunk offset table (single chunk).
func buildStco(dataOffset int64) []byte {
	body := make([]byte, 4+4+4)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1) // entry count
	binary.BigEndian.PutUint32(body[8:12], uint32(dataOffset))
	return body
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

	videoPID := uint16(0)
	audioPID := uint16(0)
	pmtPID := uint16(0)
	videoStreamType := uint8(0)
	audioStreamType := uint8(0)
	patParsed := false
	pmtParsed := false

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
		videoPID = 256
		audioPID = 257
		videoStreamType = 27
		audioStreamType = 15
		pesBuffers[videoPID] = &pesBuffer{}
		pesBuffers[audioPID] = &pesBuffer{}
	}

	// Second pass: collect PES packets
	offset = startOffset
	type pesPacket struct {
		pid  uint16
		data []byte
	}
	var pesPackets []pesPacket

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
		if pkt.payloadUnitStart && buf.started && len(buf.data) > 0 {
			pesPackets = append(pesPackets, pesPacket{pid: pkt.pid, data: buf.data})
			buf.data = nil
			buf.started = false
		}
		if pkt.payloadUnitStart {
			buf.data = pkt.payload
			buf.started = true
		} else if buf.started {
			buf.data = append(buf.data, pkt.payload...)
		}
		offset += tsPacketSize
	}

	for pid, buf := range pesBuffers {
		if buf.started && len(buf.data) > 0 {
			pesPackets = append(pesPackets, pesPacket{pid: pid, data: buf.data})
		}
	}

	var videoSamples []mp4Sample
	var audioSamples []mp4Sample

	for _, pes := range pesPackets {
		if pes.pid == videoPID {
			samples := extractSamplesFromPES(pes.data, true, cfg.timescale)
			videoSamples = append(videoSamples, samples...)
		} else if pes.pid == audioPID {
			samples := extractSamplesFromPES(pes.data, false, cfg.timescale)
			audioSamples = append(audioSamples, samples...)
		}
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
		return pkt, nil
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
	offset += 5
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
	offset += 5
	offset += 2 // PCR_PID
	if offset+2 > len(payload) {
		return 0, 0, 0, 0
	}
	programInfoLen := int(binary.BigEndian.Uint16(payload[offset:offset+2]) & 0x0fff)
	offset += 2 + programInfoLen

	tableStart := int(payload[0]) + 1
	end := tableStart + 3 + sectionLen - 4
	for i := offset; i+5 <= end && i+5 <= len(payload); {
		streamType := payload[i]
		pid := binary.BigEndian.Uint16(payload[i+1:i+3]) & 0x1fff
		esInfoLen := int(binary.BigEndian.Uint16(payload[i+3:i+5]) & 0x0fff)
		switch streamType {
		case 0x1b, 0x24:
			videoPID = pid
			videoST = streamType
		case 0x0f, 0x03, 0x04:
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
		validStream := streamID == 0xbd || streamID == 0xbe || streamID == 0xbf ||
			(streamID >= 0xc0 && streamID <= 0xef) || (streamID >= 0xf0 && streamID <= 0xfe)
		if !validStream {
			offset += 4
			continue
		}
		if offset+6 >= len(data) {
			break
		}
		pesLen := int(binary.BigEndian.Uint16(data[offset+4 : offset+6]))
		payloadStart := offset + 6
		var pts, dts int64
		if offset+9 <= len(data) {
			flags := data[offset+7]
			headerDataLen := int(data[offset+8])
			ptsPresent := (flags & 0x80) != 0
			dtsPresent := (flags & 0x40) != 0
			if ptsPresent && offset+9+5 <= len(data) {
				pts = parsePTS(data[offset+9:])
				dts = pts
			}
			if dtsPresent && offset+9+10 <= len(data) {
				dts = parsePTS(data[offset+9+5:])
			}
			payloadStart = offset + 9 + headerDataLen
		}
		payloadEnd := offset + 6 + pesLen
		if pesLen == 0 {
			payloadEnd = len(data)
		}
		if payloadEnd > len(data) {
			payloadEnd = len(data)
		}
		if payloadStart > payloadEnd {
			payloadStart = payloadEnd
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
	}

	// Fix durations: use PTS differences
	if len(samples) > 1 {
		for i := 0; i < len(samples)-1; i++ {
			diff := samples[i+1].pts - samples[i].pts
			if diff > 0 {
				samples[i].duration = uint32(diff)
			}
		}
		// Last sample gets the same duration as the second-to-last
		if len(samples) > 1 {
			samples[len(samples)-1].duration = samples[len(samples)-2].duration
		}
	}

	return samples
}

func parsePTS(data []byte) int64 {
	if len(data) < 5 {
		return 0
	}
	pts := int64((data[0]>>1)&0x07) << 30
	pts |= int64(data[1]) << 22
	pts |= int64(data[2]>>1) << 15
	pts |= int64(data[3]) << 7
	pts |= int64(data[4] >> 1)
	return pts
}

func isKeyFrame(data []byte, isVideo bool) bool {
	if !isVideo {
		return true
	}
	if len(data) < 4 {
		return false
	}
	foundSPS := false
	foundSlice := false
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
			switch nalType {
			case 5: // IDR slice
				return true
			case 7: // SPS
				foundSPS = true
			case 1, 2, 3, 4: // non-IDR slice
				foundSlice = true
			}
		}
	}
	// Only mark as keyframe if we found an explicit IDR or SPS+slice combo
	if foundSPS && foundSlice {
		return true
	}
	return false
}

// --- Box helpers (fMP4 building blocks, used by tests and mux.go) ---

// buildMvhd builds a minimal mvhd box body.
func buildMvhd(timescale uint32) []byte {
	body := make([]byte, 108)
	body[0] = 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	binary.BigEndian.PutUint32(body[16:20], 0x00010000)
	body[20] = 0x01
	body[21] = 0x00
	binary.BigEndian.PutUint32(body[104:108], 3)
	return body
}

// buildMfhd builds a movie fragment header box body.
func buildMfhd(seqNum uint32) []byte {
	body := make([]byte, 8)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], seqNum)
	return body
}

// buildTrex builds a track extends box body.
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

// buildMvex builds a movie extends box body.
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

// buildTfdt builds a track fragment decode time box body.
func buildTfdt(baseMediaDecodeTime uint64) []byte {
	body := make([]byte, 12)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:12], uint32(baseMediaDecodeTime))
	return body
}

// buildTraf builds a track fragment box body (for fMP4).
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

// buildTfhd builds a track fragment header box body.
func buildTfhd(trackID uint32) []byte {
	body := make([]byte, 12)
	body[0] = 0
	body[1] = 0x02
	binary.BigEndian.PutUint32(body[4:8], trackID)
	binary.BigEndian.PutUint32(body[8:12], 3600)
	return body
}

// buildTrun builds a track fragment run box body.
func buildTrun(sample mp4Sample, isVideo bool) []byte {
	flags := uint32(0x000001)
	if isVideo && sample.isKeyFrame {
		flags |= 0x000200
	}
	body := make([]byte, 20)
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], flags)
	binary.BigEndian.PutUint32(body[8:12], 1)
	binary.BigEndian.PutUint32(body[12:16], 0)
	binary.BigEndian.PutUint32(body[16:20], uint32(len(sample.data)))
	return body
}

// buildTkhd builds a track header box body.
func buildTkhd(trackID uint32, isVideo bool) []byte {
	body := make([]byte, 84)
	body[0] = 0
	body[3] = 0x03
	binary.BigEndian.PutUint32(body[20:24], trackID)
	if isVideo {
		binary.BigEndian.PutUint32(body[76:80], 1920<<16)
		binary.BigEndian.PutUint32(body[80:84], 1080<<16)
	}
	return body
}

// --- MP4 box I/O helpers ---

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

// buildHdlr builds the handler reference box.
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

// buildDref builds the data reference box.
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

// buildStsd builds the sample description box.
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
		binary.BigEndian.PutUint16(entry[16:18], 2)
		binary.BigEndian.PutUint16(entry[18:20], 16)
		binary.BigEndian.PutUint32(entry[24:28], 0xac440000) // 44100 Hz
	}
	body := make([]byte, 8+len(entry))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1)
	copy(body[8:], entry)
	return body
}


