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

	// Build moov payload first to know its size. The number of chunk offsets is
	// already known, only their absolute values change after moov size is known.
	layout := buildMediaLayout(videoSamples, audioSamples, 0)
	moovPayload := buildMoovBox(cfg, videoSamples, audioSamples, videoTrack, audioTrack, videoTrackID, audioTrackID, layout)

	// ftyp box is 8-byte header + 20-byte payload.
	ftypSize := int64(28)
	moovSize := int64(8 + len(moovPayload))
	mdatHeaderSize := int64(8)
	if mdatSize+8 > 0xFFFFFFFF {
		moovSize = int64(8 + len(moovPayload))
		mdatHeaderSize = 16
	}
	mdatOffset := ftypSize + moovSize

	// Rebuild moov with correct stco offset (mdatOffset + mdatHeaderSize)
	dataStartInMdat := mdatOffset + mdatHeaderSize
	layout = buildMediaLayout(videoSamples, audioSamples, dataStartInMdat)
	moovPayload = buildMoovBox(cfg, videoSamples, audioSamples, videoTrack, audioTrack, videoTrackID, audioTrackID, layout)

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

	// Write media data in timestamp order so players can seek without scanning
	// from one huge video chunk to one huge audio chunk.
	for _, entry := range layout.entries {
		var data []byte
		if entry.isVideo {
			data = videoSamples[entry.index].data
		} else {
			data = audioSamples[entry.index].data
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}

	return nil
}

type mediaLayout struct {
	entries      []mediaLayoutEntry
	videoOffsets []int64
	audioOffsets []int64
}

type mediaLayoutEntry struct {
	isVideo bool
	index   int
}

func buildMediaLayout(videoSamples, audioSamples []mp4Sample, dataStart int64) mediaLayout {
	layout := mediaLayout{
		videoOffsets: make([]int64, len(videoSamples)),
		audioOffsets: make([]int64, len(audioSamples)),
	}

	v, a := 0, 0
	offset := dataStart
	for v < len(videoSamples) || a < len(audioSamples) {
		writeVideo := false
		if v < len(videoSamples) && a < len(audioSamples) {
			writeVideo = videoSamples[v].pts <= audioSamples[a].pts
		} else {
			writeVideo = v < len(videoSamples)
		}

		if writeVideo {
			layout.videoOffsets[v] = offset
			layout.entries = append(layout.entries, mediaLayoutEntry{isVideo: true, index: v})
			offset += int64(len(videoSamples[v].data))
			v++
		} else {
			layout.audioOffsets[a] = offset
			layout.entries = append(layout.entries, mediaLayoutEntry{index: a})
			offset += int64(len(audioSamples[a].data))
			a++
		}
	}

	return layout
}

func writeBoxHeader(w io.Writer, boxType string, size uint32) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], boxType)
	_, err := w.Write(hdr)
	return err
}

// buildMoovBox builds the entire moov box payload with proper sample tables.
func buildMoovBox(cfg *remuxConfig, videoSamples, audioSamples []mp4Sample, videoTrack, audioTrack *trackInfo, videoTrackID, audioTrackID uint32, layout mediaLayout) []byte {
	var moovBody []byte

	// mvhd
	mvhd := buildMvhdFull(cfg.timescale, videoTrackID, audioTrackID, movieDuration(videoSamples, audioSamples))
	mvhdBox := make([]byte, 8+len(mvhd))
	binary.BigEndian.PutUint32(mvhdBox[0:4], uint32(len(mvhdBox)))
	copy(mvhdBox[4:8], "mvhd")
	copy(mvhdBox[8:], mvhd)
	moovBody = append(moovBody, mvhdBox...)

	if videoTrack != nil && len(videoSamples) > 0 {
		trak := buildTrakFull(cfg, videoTrack, videoTrackID, true, videoSamples, layout.videoOffsets)
		moovBody = append(moovBody, trak...)
	}

	// audio trak
	if audioTrack != nil && len(audioSamples) > 0 {
		trak := buildTrakFull(cfg, audioTrack, audioTrackID, false, audioSamples, layout.audioOffsets)
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

func buildMvhdFull(timescale uint32, videoTrackID, audioTrackID uint32, duration int64) []byte {
	body := make([]byte, 100)
	body[0] = 0 // version 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	if duration > 0 {
		binary.BigEndian.PutUint32(body[16:20], uint32(duration))
	}
	binary.BigEndian.PutUint32(body[20:24], 0x00010000) // rate = 1.0
	body[24] = 0x01                                     // volume = 1.0
	body[25] = 0x00
	writeUnityMatrix(body[36:72])
	// next_track_ID = max(trackIDs) + 1
	nextID := videoTrackID
	if audioTrackID > nextID {
		nextID = audioTrackID
	}
	binary.BigEndian.PutUint32(body[96:100], nextID+1)
	return body
}

func movieDuration(videoSamples, audioSamples []mp4Sample) int64 {
	videoDur := sampleDuration(videoSamples)
	audioDur := sampleDuration(audioSamples)
	if audioDur > videoDur {
		return audioDur
	}
	return videoDur
}

func buildTrakFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, chunkOffsets []int64) []byte {
	var trakBody []byte

	// tkhd
	tkhd := buildTkhdFull(trackID, isVideo, track, cfg.timescale, samples)
	tkhdBox := make([]byte, 8+len(tkhd))
	binary.BigEndian.PutUint32(tkhdBox[0:4], uint32(len(tkhdBox)))
	copy(tkhdBox[4:8], "tkhd")
	copy(tkhdBox[8:], tkhd)
	trakBody = append(trakBody, tkhdBox...)

	// mdia
	mdia := buildMdiaFull(cfg, track, trackID, isVideo, samples, chunkOffsets)
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
	binary.BigEndian.PutUint32(body[12:16], trackID)

	// duration (in movie timescale)
	if len(samples) > 0 {
		duration := sampleDuration(samples)
		if duration > 0 {
			binary.BigEndian.PutUint32(body[20:24], uint32(duration))
		}
	}

	if !isVideo {
		body[36] = 0x01 // volume = 1.0 for audio
	}
	writeUnityMatrix(body[40:76])
	if isVideo {
		binary.BigEndian.PutUint32(body[76:80], uint32(track.width)<<16)
		binary.BigEndian.PutUint32(body[80:84], uint32(track.height)<<16)
	}
	return body
}

func buildMdiaFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, chunkOffsets []int64) []byte {
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
	minf := buildMinfFull(cfg, track, trackID, isVideo, samples, chunkOffsets)
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
		dur := sampleDuration(samples)
		binary.BigEndian.PutUint32(body[16:20], uint32(dur))
	}
	body[20] = 0x55
	body[21] = 0xc4 // language = "und"
	return body
}

func sampleDuration(samples []mp4Sample) int64 {
	if len(samples) == 0 {
		return 0
	}
	first := samples[0].pts
	last := samples[len(samples)-1]
	duration := last.pts - first + int64(last.duration)
	if duration < 0 {
		return int64(len(samples)) * int64(last.duration)
	}
	return duration
}

func writeUnityMatrix(dst []byte) {
	if len(dst) < 36 {
		return
	}
	binary.BigEndian.PutUint32(dst[0:4], 0x00010000)
	binary.BigEndian.PutUint32(dst[16:20], 0x00010000)
	binary.BigEndian.PutUint32(dst[32:36], 0x40000000)
}

func buildMinfFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, chunkOffsets []int64) []byte {
	var minfBody []byte

	if isVideo {
		vmhd := make([]byte, 12)
		vmhd[0] = 0
		vmhd[3] = 0x01
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

	stbl := buildStblFull(cfg, track, trackID, isVideo, samples, chunkOffsets)
	minfBody = append(minfBody, stbl...)

	result := make([]byte, 8+len(minfBody))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(result)))
	copy(result[4:8], "minf")
	copy(result[8:], minfBody)
	return result
}

// buildStblFull builds a proper stbl box with stts, stsc, stsz, stco fully populated.
func buildStblFull(cfg *remuxConfig, track *trackInfo, trackID uint32, isVideo bool, samples []mp4Sample, chunkOffsets []int64) []byte {
	var stblBody []byte

	// stsd — now with proper codec config boxes (avcC / esds)
	stsd := buildStsd(cfg, track, isVideo, samples)
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
	stsc := buildStsc(1)
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
	stco := buildStco(chunkOffsets)
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
	binary.BigEndian.PutUint32(body[4:8], 1)                     // entry count
	binary.BigEndian.PutUint32(body[8:12], 1)                    // first chunk
	binary.BigEndian.PutUint32(body[12:16], uint32(sampleCount)) // samples_per_chunk
	binary.BigEndian.PutUint32(body[16:20], 1)                   // sample description index
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

// buildStco creates the chunk offset table.
func buildStco(offsets []int64) []byte {
	body := make([]byte, 4+4+4*len(offsets))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], uint32(len(offsets)))
	for i, offset := range offsets {
		binary.BigEndian.PutUint32(body[8+i*4:12+i*4], uint32(offset))
	}
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
	// Codec config extracted from stream
	avcConfig  []byte // AVCDecoderConfigurationRecord (avcC payload)
	hevcConfig []byte // HEVCDecoderConfigurationRecord (hvcC payload)
	aacConfig  []byte // AudioSpecificConfig (esds payload)
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
	var avcConfig []byte
	var hevcConfig []byte
	var aacConfig []byte
	videoCodec := cfg.videoCodec
	if videoStreamType == 0x24 {
		videoCodec = "h265"
	}

	for _, pes := range pesPackets {
		if pes.pid == videoPID {
			samples, videoCfg := extractSamplesFromPES(pes.data, true, cfg.timescale, videoCodec)
			videoSamples = append(videoSamples, samples...)
			if videoCodec == "h265" {
				if hevcConfig == nil && len(videoCfg) > 0 {
					hevcConfig = videoCfg
				}
			} else if avcConfig == nil && len(videoCfg) > 0 {
				avcConfig = videoCfg
			}
		} else if pes.pid == audioPID {
			samples, aacCfg := extractSamplesFromPES(pes.data, false, cfg.timescale, cfg.videoCodec)
			audioSamples = append(audioSamples, samples...)
			if aacConfig == nil && len(aacCfg) > 0 {
				aacConfig = aacCfg
			}
		}
	}

	videoWidth := uint16(1920)
	videoHeight := uint16(1080)
	if videoCodec == "h265" {
		if width, height, ok := parseHEVCConfigDimensions(hevcConfig); ok {
			videoWidth = width
			videoHeight = height
		}
	} else if width, height, ok := parseAVCConfigDimensions(avcConfig); ok {
		videoWidth = width
		videoHeight = height
	}

	videoTrack := &trackInfo{
		pid:        videoPID,
		streamType: videoStreamType,
		width:      videoWidth,
		height:     videoHeight,
		avcConfig:  avcConfig,
		hevcConfig: hevcConfig,
	}
	audioTrack := &trackInfo{
		pid:        audioPID,
		streamType: audioStreamType,
		aacConfig:  aacConfig,
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

// extractSamplesFromPES parses PES packets and extracts media samples.
// For video, it also extracts codec parameter sets to build avcC/hvcC config.
// For audio, it extracts AudioSpecificConfig from ADTS headers.
func extractSamplesFromPES(data []byte, isVideo bool, timescale uint32, videoCodec string) ([]mp4Sample, []byte) {
	var samples []mp4Sample
	var codecConfig []byte // avcC/hvcC for video, AudioSpecificConfig for audio

	// Track parameter sets for video codec config.
	var vpsData []byte
	var spsData []byte
	var ppsData []byte

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

			if isVideo {
				if videoCodec == "h265" {
					vps, sps, pps := extractHEVCParameterSets(payload)
					if len(vps) > 0 {
						vpsData = vps
					}
					if len(sps) > 0 {
						spsData = sps
					}
					if len(pps) > 0 {
						ppsData = pps
					}
				} else {
					sps, pps := extractSPSPPS(payload)
					if len(sps) > 0 {
						spsData = sps
					}
					if len(pps) > 0 {
						ppsData = pps
					}
				}
				keyFrame := isKeyFrame(payload, true)
				payload = annexBToAVCC(payload)
				sample := mp4Sample{
					data:       payload,
					pts:        pts,
					dts:        dts,
					duration:   3600,
					isKeyFrame: keyFrame,
				}
				samples = append(samples, sample)
				offset = payloadEnd
				continue
			} else {
				aacSamples, aacCfg := extractAACSamplesFromADTS(payload, pts, timescale)
				if len(aacCfg) > 0 && codecConfig == nil {
					codecConfig = aacCfg
				}
				if len(aacSamples) > 0 {
					samples = append(samples, aacSamples...)
					offset = payloadEnd
					continue
				}
			}

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
		if len(samples) > 1 {
			samples[len(samples)-1].duration = samples[len(samples)-2].duration
		}
	}

	// Build codec config from extracted parameter sets.
	if isVideo && videoCodec == "h265" && len(spsData) > 0 {
		codecConfig = buildHEVCConfigRecord(vpsData, spsData, ppsData)
	} else if isVideo && len(spsData) > 0 {
		codecConfig = buildAVCConfigRecord(spsData, ppsData)
	}

	return samples, codecConfig
}

// annexBToAVCC converts H.264 Annex-B start-code NAL units to MP4 length-prefixed samples.
func annexBToAVCC(data []byte) []byte {
	nals := findNALUnits(data)
	if len(nals) == 0 {
		return data
	}

	var out []byte
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(nal)))
		out = append(out, hdr...)
		out = append(out, nal...)
	}
	if len(out) == 0 {
		return data
	}
	return out
}

// extractSPSPPS extracts SPS and PPS NAL units from H.264 access unit data.
func extractSPSPPS(data []byte) (sps, pps []byte) {
	nals := findNALUnits(data)
	for _, nal := range nals {
		if len(nal) < 1 {
			continue
		}
		nalType := nal[0] & 0x1f
		switch nalType {
		case 7: // SPS
			sps = make([]byte, len(nal))
			copy(sps, nal)
		case 8: // PPS
			pps = make([]byte, len(nal))
			copy(pps, nal)
		}
	}
	return
}

// extractHEVCParameterSets extracts HEVC VPS/SPS/PPS NAL units.
func extractHEVCParameterSets(data []byte) (vps, sps, pps []byte) {
	nals := findNALUnits(data)
	if len(nals) == 0 {
		nals = findAVCCNALUnits(data)
	}
	for _, nal := range nals {
		if len(nal) < 2 {
			continue
		}
		nalType := (nal[0] >> 1) & 0x3f
		switch nalType {
		case 32: // VPS
			vps = append([]byte(nil), nal...)
		case 33: // SPS
			sps = append([]byte(nil), nal...)
		case 34: // PPS
			pps = append([]byte(nil), nal...)
		}
	}
	return
}

// findNALUnits finds all NAL unit payloads (without start code) in data.
func findNALUnits(data []byte) [][]byte {
	var nals [][]byte
	i := 0
	for i < len(data) {
		// Find start code (0x00 0x00 0x01 or 0x00 0x00 0x00 0x01)
		startCodeLen := 0
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			startCodeLen = 3
		} else if i+3 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			startCodeLen = 4
		}
		if startCodeLen == 0 {
			i++
			continue
		}
		nalStart := i + startCodeLen
		// Find next start code or end of data
		nalEnd := len(data)
		for j := nalStart + 1; j < len(data)-2; j++ {
			if data[j] == 0 && data[j+1] == 0 {
				if j+2 < len(data) && data[j+2] == 1 {
					nalEnd = j
					break
				}
				if j+3 < len(data) && data[j+2] == 0 && data[j+3] == 1 {
					nalEnd = j
					break
				}
			}
		}
		if nalStart < nalEnd {
			// Remove trailing zeros
			nalData := data[nalStart:nalEnd]
			for len(nalData) > 0 && nalData[len(nalData)-1] == 0 {
				nalData = nalData[:len(nalData)-1]
			}
			if len(nalData) > 0 {
				nals = append(nals, nalData)
			}
		}
		i = nalEnd
	}
	return nals
}

// buildAVCConfigRecord builds an AVCDecoderConfigurationRecord (avcC box payload).
func buildAVCConfigRecord(sps, pps []byte) []byte {
	// Parse SPS to extract profile, compat, level
	profile := uint8(0x64) // default: High
	compat := uint8(0x00)
	level := uint8(0x1e) // default: 3.0
	if len(sps) >= 4 {
		profile = sps[1]
		compat = sps[2]
		level = sps[3]
	}

	// avcC box structure:
	// configurationVersion = 1
	// AVCProfileIndication
	// profile_compatibility
	// AVCLevelIndication
	// lengthSizeMinusOne = 3 (4 bytes NAL length)
	// numOfSequenceParameterSets = 1
	// spsLength (2 bytes) + sps data
	// numOfPictureParameterSets = 1
	// ppsLength (2 bytes) + pps data
	size := 6 + 2 + len(sps) + 1 + 2 + len(pps)
	buf := make([]byte, size)
	buf[0] = 1 // configurationVersion
	buf[1] = profile
	buf[2] = compat
	buf[3] = level
	buf[4] = 0xff // lengthSizeMinusOne = 3 (0b11111100 | 0x03)
	// numOfSPS = 1
	buf[5] = 0xe1 // 0b11100000 | 1
	// sps length
	binary.BigEndian.PutUint16(buf[6:8], uint16(len(sps)))
	copy(buf[8:], sps)
	// numOfPPS = 1
	buf[8+len(sps)] = 1
	// pps length
	binary.BigEndian.PutUint16(buf[9+len(sps):11+len(sps)], uint16(len(pps)))
	copy(buf[11+len(sps):], pps)
	return buf
}

// buildHEVCConfigRecord builds an HEVCDecoderConfigurationRecord (hvcC payload).
func buildHEVCConfigRecord(vps, sps, pps []byte) []byte {
	info := parseHEVCProfileTierLevel(sps)
	arrays := make([][]byte, 0, 3)
	if len(vps) > 0 {
		arrays = append(arrays, buildHEVCConfigArray(32, vps))
	}
	if len(sps) > 0 {
		arrays = append(arrays, buildHEVCConfigArray(33, sps))
	}
	if len(pps) > 0 {
		arrays = append(arrays, buildHEVCConfigArray(34, pps))
	}

	size := 23
	for _, arr := range arrays {
		size += len(arr)
	}
	buf := make([]byte, size)
	buf[0] = 1 // configurationVersion
	buf[1] = info.profileByte
	copy(buf[2:6], info.compatibility[:])
	copy(buf[6:12], info.constraint[:])
	buf[12] = info.levelIDC
	binary.BigEndian.PutUint16(buf[13:15], 0xf000) // min_spatial_segmentation_idc
	buf[15] = 0xfc                                 // parallelismType unknown
	buf[16] = 0xfc | info.chromaFormatIDC
	buf[17] = 0xf8 | info.bitDepthLumaMinus8
	buf[18] = 0xf8 | info.bitDepthChromaMinus8
	binary.BigEndian.PutUint16(buf[19:21], 0) // avgFrameRate
	buf[21] = ((info.numTemporalLayers & 0x07) << 3) | ((info.temporalIDNested & 0x01) << 2) | 0x03
	buf[22] = byte(len(arrays))
	p := 23
	for _, arr := range arrays {
		copy(buf[p:], arr)
		p += len(arr)
	}
	return buf
}

func buildHEVCConfigArray(nalType uint8, nal []byte) []byte {
	buf := make([]byte, 1+2+2+len(nal))
	buf[0] = 0x80 | (nalType & 0x3f) // array_completeness + NAL type
	binary.BigEndian.PutUint16(buf[1:3], 1)
	binary.BigEndian.PutUint16(buf[3:5], uint16(len(nal)))
	copy(buf[5:], nal)
	return buf
}

type hevcProfileInfo struct {
	profileByte          byte
	compatibility        [4]byte
	constraint           [6]byte
	levelIDC             byte
	numTemporalLayers    byte
	temporalIDNested     byte
	chromaFormatIDC      byte
	bitDepthLumaMinus8   byte
	bitDepthChromaMinus8 byte
}

func parseHEVCProfileTierLevel(sps []byte) hevcProfileInfo {
	info := hevcProfileInfo{
		profileByte:          1, // Main profile
		levelIDC:             120,
		numTemporalLayers:    1,
		temporalIDNested:     1,
		chromaFormatIDC:      1,
		bitDepthLumaMinus8:   0,
		bitDepthChromaMinus8: 0,
	}
	if len(sps) < 14 {
		return info
	}
	rbsp := removeEmulationPreventionBytes(sps[2:])
	if len(rbsp) < 13 {
		return info
	}
	info.numTemporalLayers = ((rbsp[0] >> 1) & 0x07) + 1
	info.temporalIDNested = rbsp[0] & 0x01
	info.profileByte = rbsp[1]
	copy(info.compatibility[:], rbsp[2:6])
	copy(info.constraint[:], rbsp[6:12])
	info.levelIDC = rbsp[12]

	if chroma, bitDepthLuma, bitDepthChroma, ok := parseHEVCSPSFormat(sps); ok {
		info.chromaFormatIDC = byte(chroma & 0x03)
		info.bitDepthLumaMinus8 = byte(bitDepthLuma & 0x07)
		info.bitDepthChromaMinus8 = byte(bitDepthChroma & 0x07)
	}
	return info
}

func parseAVCConfigDimensions(avcC []byte) (uint16, uint16, bool) {
	if len(avcC) < 8 {
		return 0, 0, false
	}
	spsCount := int(avcC[5] & 0x1f)
	if spsCount == 0 {
		return 0, 0, false
	}
	spsLen := int(binary.BigEndian.Uint16(avcC[6:8]))
	if 8+spsLen > len(avcC) {
		return 0, 0, false
	}
	return parseH264SPSDimensions(avcC[8 : 8+spsLen])
}

func parseHEVCConfigDimensions(hvcC []byte) (uint16, uint16, bool) {
	if len(hvcC) < 23 {
		return 0, 0, false
	}
	numArrays := int(hvcC[22])
	offset := 23
	for i := 0; i < numArrays && offset+3 <= len(hvcC); i++ {
		nalType := hvcC[offset] & 0x3f
		numNalus := int(binary.BigEndian.Uint16(hvcC[offset+1 : offset+3]))
		offset += 3
		for j := 0; j < numNalus && offset+2 <= len(hvcC); j++ {
			nalLen := int(binary.BigEndian.Uint16(hvcC[offset : offset+2]))
			offset += 2
			if offset+nalLen > len(hvcC) {
				return 0, 0, false
			}
			if nalType == 33 {
				return parseHEVCSPSDimensions(hvcC[offset : offset+nalLen])
			}
			offset += nalLen
		}
	}
	return 0, 0, false
}

func parseH264SPSDimensions(sps []byte) (uint16, uint16, bool) {
	if len(sps) < 4 {
		return 0, 0, false
	}
	rbsp := removeEmulationPreventionBytes(sps[1:])
	br := newBitReader(rbsp)

	profileIDC := sps[1]
	br.skipBits(8) // constraint flags + reserved
	br.skipBits(8) // level_idc
	br.readUE()    // seq_parameter_set_id

	chromaFormatIDC := uint(1)
	switch profileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chromaFormatIDC = br.readUE()
		if chromaFormatIDC == 3 {
			br.skipBits(1) // separate_colour_plane_flag
		}
		br.readUE() // bit_depth_luma_minus8
		br.readUE() // bit_depth_chroma_minus8
		br.skipBits(1)
		if br.readBit() == 1 {
			scalingCount := 8
			if chromaFormatIDC == 3 {
				scalingCount = 12
			}
			for i := 0; i < scalingCount; i++ {
				if br.readBit() == 1 {
					skipScalingList(&br, i < 6)
				}
			}
		}
	}

	br.readUE() // log2_max_frame_num_minus4
	picOrderCntType := br.readUE()
	if picOrderCntType == 0 {
		br.readUE()
	} else if picOrderCntType == 1 {
		br.skipBits(1)
		br.readSE()
		br.readSE()
		cycle := br.readUE()
		for i := uint(0); i < cycle; i++ {
			br.readSE()
		}
	}
	br.readUE()    // max_num_ref_frames
	br.skipBits(1) // gaps_in_frame_num_value_allowed_flag
	picWidth := br.readUE() + 1
	picHeight := br.readUE() + 1
	frameMbsOnly := br.readBit()
	if frameMbsOnly == 0 {
		br.skipBits(1)
	}
	br.skipBits(1) // direct_8x8_inference_flag

	frameCropLeft, frameCropRight, frameCropTop, frameCropBottom := uint(0), uint(0), uint(0), uint(0)
	if br.readBit() == 1 {
		frameCropLeft = br.readUE()
		frameCropRight = br.readUE()
		frameCropTop = br.readUE()
		frameCropBottom = br.readUE()
	}

	width := int(picWidth * 16)
	height := int(picHeight * 16 * (2 - frameMbsOnly))
	cropUnitX, cropUnitY := 1, 2
	if chromaFormatIDC == 1 {
		cropUnitX = 2
		cropUnitY = 2 * int(2-frameMbsOnly)
	} else if chromaFormatIDC == 2 {
		cropUnitX = 2
		cropUnitY = int(2 - frameMbsOnly)
	} else if chromaFormatIDC == 3 {
		cropUnitX = 1
		cropUnitY = int(2 - frameMbsOnly)
	}
	width -= int(frameCropLeft+frameCropRight) * cropUnitX
	height -= int(frameCropTop+frameCropBottom) * cropUnitY
	if width <= 0 || height <= 0 || width > 65535 || height > 65535 {
		return 0, 0, false
	}
	return uint16(width), uint16(height), true
}

func parseHEVCSPSFormat(sps []byte) (chromaFormatIDC, bitDepthLumaMinus8, bitDepthChromaMinus8 uint, ok bool) {
	_, _, chroma, bitDepthLuma, bitDepthChroma, ok := parseHEVCSPS(sps)
	return chroma, bitDepthLuma, bitDepthChroma, ok
}

func parseHEVCSPSDimensions(sps []byte) (uint16, uint16, bool) {
	width, height, _, _, _, ok := parseHEVCSPS(sps)
	if !ok || width <= 0 || height <= 0 || width > 65535 || height > 65535 {
		return 0, 0, false
	}
	return uint16(width), uint16(height), true
}

func parseHEVCSPS(sps []byte) (width, height int, chromaFormatIDC, bitDepthLumaMinus8, bitDepthChromaMinus8 uint, ok bool) {
	if len(sps) < 4 {
		return 0, 0, 0, 0, 0, false
	}
	rbsp := removeEmulationPreventionBytes(sps[2:])
	br := newBitReader(rbsp)
	br.skipBits(4) // sps_video_parameter_set_id
	maxSubLayersMinus1 := br.readBits(3)
	br.skipBits(1) // sps_temporal_id_nesting_flag
	skipHEVCProfileTierLevel(&br, maxSubLayersMinus1)
	br.readUE() // sps_seq_parameter_set_id
	chromaFormatIDC = br.readUE()
	if chromaFormatIDC == 3 {
		br.skipBits(1) // separate_colour_plane_flag
	}
	picWidth := br.readUE()
	picHeight := br.readUE()
	confLeft, confRight, confTop, confBottom := uint(0), uint(0), uint(0), uint(0)
	if br.readBit() == 1 {
		confLeft = br.readUE()
		confRight = br.readUE()
		confTop = br.readUE()
		confBottom = br.readUE()
	}
	bitDepthLumaMinus8 = br.readUE()
	bitDepthChromaMinus8 = br.readUE()

	subWidthC, subHeightC := uint(1), uint(1)
	switch chromaFormatIDC {
	case 1:
		subWidthC, subHeightC = 2, 2
	case 2:
		subWidthC, subHeightC = 2, 1
	}
	cropWidth := (confLeft + confRight) * subWidthC
	cropHeight := (confTop + confBottom) * subHeightC
	if picWidth <= cropWidth || picHeight <= cropHeight {
		return 0, 0, 0, 0, 0, false
	}
	width = int(picWidth - cropWidth)
	height = int(picHeight - cropHeight)
	return width, height, chromaFormatIDC, bitDepthLumaMinus8, bitDepthChromaMinus8, true
}

func skipHEVCProfileTierLevel(br *bitReader, maxSubLayersMinus1 uint) {
	br.skipBits(2 + 1 + 5) // profile_space, tier_flag, profile_idc
	br.skipBits(32)        // profile_compatibility_flags
	br.skipBits(48)        // constraint_indicator_flags
	br.skipBits(8)         // level_idc

	subLayerProfilePresent := make([]uint, maxSubLayersMinus1)
	subLayerLevelPresent := make([]uint, maxSubLayersMinus1)
	for i := uint(0); i < maxSubLayersMinus1; i++ {
		subLayerProfilePresent[i] = br.readBit()
		subLayerLevelPresent[i] = br.readBit()
	}
	if maxSubLayersMinus1 > 0 {
		for i := maxSubLayersMinus1; i < 8; i++ {
			br.skipBits(2)
		}
	}
	for i := uint(0); i < maxSubLayersMinus1; i++ {
		if subLayerProfilePresent[i] == 1 {
			br.skipBits(2 + 1 + 5)
			br.skipBits(32)
			br.skipBits(48)
		}
		if subLayerLevelPresent[i] == 1 {
			br.skipBits(8)
		}
	}
}

func removeEmulationPreventionBytes(data []byte) []byte {
	out := make([]byte, 0, len(data))
	zeros := 0
	for _, b := range data {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type bitReader struct {
	data []byte
	pos  int
}

func newBitReader(data []byte) bitReader {
	return bitReader{data: data}
}

func (b *bitReader) readBit() uint {
	if b.pos >= len(b.data)*8 {
		return 0
	}
	v := (b.data[b.pos/8] >> (7 - uint(b.pos%8))) & 1
	b.pos++
	return uint(v)
}

func (b *bitReader) readBits(n int) uint {
	var value uint
	for i := 0; i < n; i++ {
		value = (value << 1) | b.readBit()
	}
	return value
}

func (b *bitReader) skipBits(n int) {
	b.pos += n
	if b.pos > len(b.data)*8 {
		b.pos = len(b.data) * 8
	}
}

func (b *bitReader) readUE() uint {
	zeros := 0
	for b.pos < len(b.data)*8 && b.readBit() == 0 {
		zeros++
	}
	value := uint(1)
	for i := 0; i < zeros; i++ {
		value = (value << 1) | b.readBit()
	}
	return value - 1
}

func (b *bitReader) readSE() int {
	ue := int(b.readUE())
	if ue%2 == 0 {
		return -(ue / 2)
	}
	return (ue + 1) / 2
}

func skipScalingList(br *bitReader, small bool) {
	size := 64
	if small {
		size = 16
	}
	lastScale, nextScale := 8, 8
	for j := 0; j < size; j++ {
		if nextScale != 0 {
			deltaScale := br.readSE()
			nextScale = (lastScale + deltaScale + 256) % 256
		}
		if nextScale != 0 {
			lastScale = nextScale
		}
	}
}

// extractADTSConfig extracts AudioSpecificConfig from an ADTS frame header.
// ADTS header is 7 bytes (no CRC) or 9 bytes (with CRC).
func extractADTSConfig(data []byte) []byte {
	if len(data) < 7 {
		return nil
	}
	// Check ADTS sync word (0xFFF)
	if data[0] != 0xff || (data[1]&0xf0) != 0xf0 {
		return nil
	}
	// MPEG version: bit 19 (0=MPEG-4, 1=MPEG-2)
	// profile: bits 17-18 (0=main, 1=LC, 2=SSR, 3=reserved)
	// sampling freq index: bits 12-15
	// channel config: bits 9-11
	profile := (data[2] >> 6) & 0x03                                   // 2 bits
	sampleRateIndex := (data[2] >> 2) & 0x0f                           // 4 bits
	channelConfig := ((data[2] & 0x01) << 2) | ((data[3] >> 6) & 0x03) // 3 bits

	// AudioSpecificConfig (ISO 14496-3):
	// 5 bits: audioObjectType (profile + 1 for LC)
	// 4 bits: samplingFrequencyIndex
	// 4 bits: channelConfiguration
	// padding
	audioObjectType := profile + 1 // AAC-LC = 2
	if audioObjectType > 31 {
		audioObjectType = 2
	}

	asc := make([]byte, 2)
	asc[0] = (audioObjectType << 3) | (sampleRateIndex >> 1)
	asc[1] = (sampleRateIndex << 7) | (channelConfig << 3)
	return asc
}

func extractAACSamplesFromADTS(data []byte, pts int64, timescale uint32) ([]mp4Sample, []byte) {
	var samples []mp4Sample
	var config []byte
	offset := 0
	duration := uint32(0)

	for offset+7 <= len(data) {
		if data[offset] != 0xff || (data[offset+1]&0xf0) != 0xf0 {
			offset++
			continue
		}

		protectionAbsent := (data[offset+1] & 0x01) != 0
		headerLen := 7
		if !protectionAbsent {
			headerLen = 9
		}
		if offset+headerLen > len(data) {
			break
		}

		frameLen := int(data[offset+3]&0x03)<<11 |
			int(data[offset+4])<<3 |
			int((data[offset+5]&0xe0)>>5)
		if frameLen < headerLen || offset+frameLen > len(data) {
			break
		}

		if config == nil {
			config = extractADTSConfig(data[offset:])
			if len(config) > 0 {
				duration = aacSampleDuration(config, timescale)
			}
		}
		if duration == 0 {
			duration = aacSampleDuration(config, timescale)
		}

		raw := make([]byte, frameLen-headerLen)
		copy(raw, data[offset+headerLen:offset+frameLen])
		samplePTS := pts + int64(len(samples))*int64(duration)
		samples = append(samples, mp4Sample{
			data:       raw,
			pts:        samplePTS,
			dts:        samplePTS,
			duration:   duration,
			isKeyFrame: true,
		})

		offset += frameLen
	}

	return samples, config
}

func aacSampleDuration(audioSpecificConfig []byte, timescale uint32) uint32 {
	sampleRate := uint32(44100)
	if len(audioSpecificConfig) >= 2 {
		srIndex := ((audioSpecificConfig[0] & 0x07) << 1) | ((audioSpecificConfig[1] >> 7) & 0x01)
		srTable := []uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
		if int(srIndex) < len(srTable) {
			sampleRate = srTable[srIndex]
		}
	}
	if sampleRate == 0 {
		return 1024
	}
	return uint32(uint64(1024) * uint64(timescale) / uint64(sampleRate))
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
	for _, nal := range findAVCCNALUnits(data) {
		if isIDRNAL(nal) {
			return true
		}
	}
	for _, nal := range findNALUnits(data) {
		if isIDRNAL(nal) {
			return true
		}
	}
	return false
}

func findAVCCNALUnits(data []byte) [][]byte {
	var nals [][]byte
	for offset := 0; offset < len(data); {
		if offset+4 > len(data) {
			return nil
		}
		nalLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if nalLen <= 0 || offset+nalLen > len(data) {
			return nil
		}
		nals = append(nals, data[offset:offset+nalLen])
		offset += nalLen
	}
	return nals
}

func isIDRNAL(nal []byte) bool {
	if len(nal) == 0 {
		return false
	}
	// H.264 IDR slice.
	if nal[0]&0x1f == 5 {
		return true
	}
	// HEVC IDR_W_RADL / IDR_N_LP.
	if ((nal[0]>>1)&0x3f) == 19 || ((nal[0]>>1)&0x3f) == 20 {
		return true
	}
	return false
}

// --- buildStsd with proper codec config boxes ---

// buildStsd builds the sample description box with avcC (video) or esds (audio).
func buildStsd(cfg *remuxConfig, track *trackInfo, isVideo bool, samples []mp4Sample) []byte {
	var entry []byte
	if isVideo {
		entry = buildVideoSampleEntry(cfg, track, samples)
	} else {
		entry = buildAudioSampleEntry(cfg, track, samples)
	}

	body := make([]byte, 8+len(entry))
	body[0] = 0
	binary.BigEndian.PutUint32(body[4:8], 1) // entry count
	copy(body[8:], entry)
	return body
}

// buildVideoSampleEntry builds an avc1/hev1 entry with avcC/hvcC box inside.
func buildVideoSampleEntry(cfg *remuxConfig, track *trackInfo, samples []mp4Sample) []byte {
	codec := [4]byte{'a', 'v', 'c', '1'}
	videoCodec := cfg.videoCodec
	if track.streamType == 0x24 {
		videoCodec = "h265"
	}
	if videoCodec == "h265" {
		codec = [4]byte{'h', 'e', 'v', '1'}
	}

	configType := "avcC"
	configData := track.avcConfig
	if videoCodec == "h265" {
		configType = "hvcC"
		configData = track.hevcConfig
		if len(configData) == 0 {
			for i := 0; i < len(samples) && i < 10 && len(configData) == 0; i++ {
				vps, sps, pps := extractHEVCParameterSets(samples[i].data)
				if len(sps) > 0 {
					configData = buildHEVCConfigRecord(vps, sps, pps)
				}
			}
		}
	} else if len(configData) == 0 {
		for i := 0; i < len(samples) && i < 10 && len(configData) == 0; i++ {
			sps, pps := extractSPSPPS(samples[i].data)
			if len(sps) > 0 {
				configData = buildAVCConfigRecord(sps, pps)
			}
		}
	}

	configBoxSize := 8 + len(configData)

	// Visual Sample Entry (ISO 14496-12):
	// 6 bytes: reserved
	// 2 bytes: data_reference_index
	// 2+2+4+4 bytes: pre-defined + reserved
	// 4+4 bytes: width, height
	// 4+4 bytes: horiz/vert resolution
	// 4 bytes: reserved
	// 2 bytes: frame_count
	// 32 bytes: compressorname
	// 2 bytes: depth
	// 2 bytes: pre-defined
	// + codec config box
	entrySize := 86 + configBoxSize
	entry := make([]byte, entrySize)
	binary.BigEndian.PutUint32(entry[0:4], uint32(entrySize))
	copy(entry[4:8], codec[:])
	binary.BigEndian.PutUint16(entry[14:16], 1) // data_reference_index
	binary.BigEndian.PutUint16(entry[32:34], track.width)
	binary.BigEndian.PutUint16(entry[34:36], track.height)
	binary.BigEndian.PutUint32(entry[36:40], 0x00480000) // horiz resolution 72 dpi
	binary.BigEndian.PutUint32(entry[40:44], 0x00480000) // vert resolution 72 dpi
	binary.BigEndian.PutUint16(entry[48:50], 1)          // frame_count
	binary.BigEndian.PutUint16(entry[82:84], 0x0018)     // depth = 24
	binary.BigEndian.PutUint16(entry[84:86], 0xffff)     // pre-defined

	binary.BigEndian.PutUint32(entry[86:90], uint32(configBoxSize))
	copy(entry[90:94], configType)
	copy(entry[94:], configData)

	return entry
}

// buildAudioSampleEntry builds an mp4a entry with esds box inside.
// Layout matches FFmpeg mov_write_audio_tag and ISO 14496-12 AudioSampleEntry.
func buildAudioSampleEntry(cfg *remuxConfig, track *trackInfo, samples []mp4Sample) []byte {
	// Extract AudioSpecificConfig if not already available
	aacConfig := track.aacConfig
	if len(aacConfig) == 0 {
		for i := 0; i < len(samples) && i < 10 && len(aacConfig) == 0; i++ {
			if len(samples[i].data) > 7 {
				aacConfig = extractADTSConfig(samples[i].data)
			}
		}
	}
	if len(aacConfig) == 0 {
		aacConfig = []byte{0x12, 0x08} // default: AAC-LC, 44100 Hz, stereo
	}

	esdsPayload := buildESDS(aacConfig)
	esdsBoxSize := 8 + len(esdsPayload)

	// AudioSampleEntry layout (ISO 14496-12 + QuickTime):
	// [0:4]   size (uint32)
	// [4:8]   type "mp4a"
	// [8:14]  reserved (6 bytes, from SampleEntry)
	// [14:16] data_reference_index (uint16)
	// [16:18] version (uint16) = 0
	// [18:20] revision (uint16) = 0
	// [20:24] vendor (uint32) = 0
	// [24:26] channel_count (uint16)
	// [26:28] sample_size (uint16)
	// [28:30] pre_defined (uint16) = 0
	// [30:32] reserved (uint16) = 0
	// [32:36] sample_rate (uint32, 16.16 fixed point)
	// [36:]   esds box
	entrySize := 36 + esdsBoxSize
	entry := make([]byte, entrySize)
	binary.BigEndian.PutUint32(entry[0:4], uint32(entrySize))
	copy(entry[4:8], "mp4a")
	binary.BigEndian.PutUint16(entry[14:16], 1)  // data_reference_index
	binary.BigEndian.PutUint16(entry[24:26], 2)  // channel_count (stereo)
	binary.BigEndian.PutUint16(entry[26:28], 16) // sample_size (16 bits)

	// Detect sample rate from AudioSpecificConfig
	sampleRate := uint32(44100)
	if len(aacConfig) >= 2 {
		srIndex := ((aacConfig[0] & 0x07) << 1) | ((aacConfig[1] >> 7) & 0x01)
		srTable := []uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
		if int(srIndex) < len(srTable) {
			sampleRate = srTable[srIndex]
		}
	}
	binary.BigEndian.PutUint32(entry[32:36], sampleRate<<16) // 16.16 fixed point

	// Write esds box at offset 36
	binary.BigEndian.PutUint32(entry[36:40], uint32(esdsBoxSize))
	copy(entry[40:44], "esds")
	copy(entry[44:], esdsPayload)

	return entry
}

// buildESDS builds an Elementary Stream Descriptor box payload.
func buildESDS(audioSpecificConfig []byte) []byte {
	ascLen := len(audioSpecificConfig)

	// Descriptor sizes (each includes 1 byte tag + 1 byte length + payload):
	// DecoderSpecificInfo (tag=5): 1+1+ascLen
	decSpecificSize := 2 + ascLen
	// DecoderConfigDescriptor (tag=4): 1+1 + 13 bytes content + DecoderSpecificInfo
	decConfigSize := 2 + 13 + decSpecificSize
	// SLConfigDescriptor (tag=6): 1+1+1
	slConfigSize := 2 + 1
	// ES_Descriptor (tag=3): 1+1 + 3 bytes content + DecoderConfigDescriptor + SLConfigDescriptor
	esDescSize := 2 + 3 + decConfigSize + slConfigSize

	// esds box: 4 (version+flags) + ES_Descriptor
	totalLen := 4 + esDescSize
	buf := make([]byte, totalLen)
	p := 0

	// Box version + flags
	buf[p] = 0
	p++
	buf[p] = 0
	p++
	buf[p] = 0
	p++
	buf[p] = 0
	p++

	// ES_Descriptor (tag=3)
	buf[p] = 0x03
	p++
	buf[p] = byte(esDescSize - 2)
	p++ // length excludes tag+len
	// ES_ID = 1
	buf[p] = 0x00
	p++
	buf[p] = 0x01
	p++
	// streamDependenceFlag=0, URL_Flag=0, OCRstreamFlag=0
	buf[p] = 0x00
	p++

	// DecoderConfigDescriptor (tag=4)
	buf[p] = 0x04
	p++
	buf[p] = byte(decConfigSize - 2)
	p++ // length excludes tag+len
	buf[p] = 0x40
	p++ // objectTypeIndication = 0x40 (Audio ISO 14496-3)
	buf[p] = 0x15
	p++ // streamType=5 (Audio), upstream=0, reserved=1
	// bufferSizeDB (3 bytes)
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++
	// maxBitrate
	buf[p] = 0x00
	p++
	buf[p] = 0x01
	p++ // ~65536 bps
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++
	// avgBitrate
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++
	buf[p] = 0x00
	p++

	// DecoderSpecificInfo (tag=5)
	buf[p] = 0x05
	p++
	buf[p] = byte(ascLen)
	p++ // length = ASC data length
	copy(buf[p:], audioSpecificConfig)
	p += ascLen

	// SLConfigDescriptor (tag=6)
	buf[p] = 0x06
	p++
	buf[p] = 0x01
	p++ // length = 1
	buf[p] = 0x02
	p++ // predefined (MP4)

	return buf[:p]
}

// --- Box helpers (fMP4 building blocks, used by tests and mux.go) ---

// buildMvhd builds a minimal mvhd box body.
func buildMvhd(timescale uint32) []byte {
	body := make([]byte, 100)
	body[0] = 0
	binary.BigEndian.PutUint32(body[12:16], timescale)
	binary.BigEndian.PutUint32(body[20:24], 0x00010000)
	body[24] = 0x01
	body[25] = 0x00
	writeUnityMatrix(body[36:72])
	binary.BigEndian.PutUint32(body[96:100], 3)
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
	binary.BigEndian.PutUint32(body[12:16], trackID)
	writeUnityMatrix(body[40:76])
	if isVideo {
		binary.BigEndian.PutUint32(body[76:80], 1920<<16)
		binary.BigEndian.PutUint32(body[80:84], 1080<<16)
	} else {
		body[36] = 0x01
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
	name := "SoundHandler"
	if isVideo {
		name = "VideoHandler"
	}
	body := make([]byte, 25+len(name))
	body[0] = 0
	if isVideo {
		copy(body[8:12], "vide")
	} else {
		copy(body[8:12], "soun")
	}
	copy(body[24:], name)
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
