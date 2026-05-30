package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	var (
		url         string
		outputDir   string
		saveName    string
		concurrency int
		maxSpeed    int64
		mergeMode   string
		headers     stringSlice
		keys        stringSlice
		autoSub     bool
		subOnly     bool
		showVersion bool
		svSelect    string
	)

	flag.StringVar(&url, "url", "", "M3U8/MPD/ISM URL (required)")
	flag.StringVar(&outputDir, "o", "/downloads", "Output directory")
	flag.StringVar(&saveName, "save-name", "", "Output filename (without extension)")
	flag.IntVar(&concurrency, "concurrency", 8, "Segment download concurrency")
	flag.Int64Var(&maxSpeed, "max-speed", 0, "Max download speed in bytes/sec (0=unlimited)")
	flag.StringVar(&mergeMode, "merge", "ts2mp4", "Merge mode: binary, ts2mp4, fmp4, ffmpeg")
	flag.Var(&headers, "H", "HTTP header (repeatable, format: Key: Value)")
	flag.Var(&keys, "key", "Decryption key in kid:key hex format (repeatable)")
	flag.BoolVar(&autoSub, "auto-subtitle-fix", false, "Auto-fix subtitle timing")
	flag.BoolVar(&subOnly, "sub-only", false, "Download subtitles only")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.StringVar(&svSelect, "sv", "", "Stream selection filter (e.g. best, res=\"3840*\":codecs=hvc1:for=best)")
	flag.Parse()

	if showVersion {
		fmt.Printf("GOm3u8DL %s (%s)\n", version, commit)
		os.Exit(0)
	}

	// URL can also be a positional argument
	if url == "" && flag.NArg() > 0 {
		url = flag.Arg(0)
	}

	if url == "" {
		fmt.Fprintln(os.Stderr, "Error: URL is required")
		fmt.Fprintln(os.Stderr, "Usage: m3u8dl -url <URL> [options]")
		fmt.Fprintln(os.Stderr, "       m3u8dl <URL> [options]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Parse headers
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	// Parse merge mode
	mode := model.MergeModeBinary
	switch strings.ToLower(mergeMode) {
	case "binary":
		mode = model.MergeModeBinary
	case "ts2mp4":
		mode = model.MergeModeTS2MP4
	case "fmp4":
		mode = model.MergeModeFMP4
	case "ffmpeg":
		mode = model.MergeModeFFmpeg
	default:
		fmt.Fprintf(os.Stderr, "Unknown merge mode: %s\n", mergeMode)
		os.Exit(1)
	}

	// Build engine
	engine := m3u8dl.New(
		m3u8dl.WithSegmentConcurrency(concurrency),
		m3u8dl.WithGlobalMaxSpeed(maxSpeed),
		m3u8dl.WithLogLevel(m3u8dl.LogInfo),
	)

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nInterrupted, cancelling...")
		cancel()
	}()

	// Get streams
	fmt.Printf("Parsing: %s\n", url)
	streams, err := engine.GetStreams(ctx, url, headerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(streams) == 0 {
		fmt.Fprintln(os.Stderr, "No streams found")
		os.Exit(1)
	}

	// Separate streams by media type
	var videoStreams, audioStreams []model.StreamInfo
	for _, s := range streams {
		switch s.MediaType {
		case model.MediaTypeVideo:
			videoStreams = append(videoStreams, s)
		case model.MediaTypeAudio:
			audioStreams = append(audioStreams, s)
		default:
			videoStreams = append(videoStreams, s)
		}
	}

	// Auto-generate save name if not provided
	if saveName == "" {
		saveName = generateSaveName(url, nil)
	}

	// Check if we have separate audio+video streams (common in DASH)
	hasSeparateAV := len(videoStreams) > 0 && len(audioStreams) > 0

	if hasSeparateAV {
		// Multi-stream: download video and audio separately, then mux
		downloadSeparateStreams(ctx, engine, url, videoStreams, audioStreams,
			svSelect, outputDir, saveName, headerMap, concurrency, maxSpeed,
			mode, autoSub, subOnly)
	} else {
		// Single stream: existing logic
		var selected *model.StreamInfo
		if svSelect != "" {
			selected = selectStreamByFilter(streams, svSelect)
		} else {
			for i := range streams {
				if streams[i].MediaType == model.MediaTypeVideo {
					if selected == nil || streams[i].Bandwidth > selected.Bandwidth {
						selected = &streams[i]
					}
				}
			}
			if selected == nil {
				selected = &streams[0]
			}
		}

		if selected == nil {
			fmt.Fprintln(os.Stderr, "No stream selected")
			os.Exit(1)
		}

		fmt.Printf("Selected: %s %s (%s, %d segments)\n",
			selected.Name, selected.Resolution,
			selected.FormatBandwidth(), selected.SegmentsCount)

		downloadSingleStream(ctx, engine, url, selected, outputDir, saveName,
			headerMap, concurrency, maxSpeed, mode, autoSub, subOnly)
	}
}

// downloadSeparateStreams downloads video and audio streams and muxes directly
// from segments (no intermediate merged files).
func downloadSeparateStreams(ctx context.Context, engine *m3u8dl.Engine, url string,
	videoStreams, audioStreams []model.StreamInfo, svSelect, outputDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	autoSub, subOnly bool) {

	// Select best video
	var selectedVideo *model.StreamInfo
	if svSelect != "" {
		selectedVideo = selectStreamByFilter(videoStreams, svSelect)
	} else {
		for i := range videoStreams {
			if selectedVideo == nil || videoStreams[i].Bandwidth > selectedVideo.Bandwidth {
				selectedVideo = &videoStreams[i]
			}
		}
	}

	// Select best audio (highest bandwidth)
	var selectedAudio *model.StreamInfo
	for i := range audioStreams {
		if selectedAudio == nil || audioStreams[i].Bandwidth > selectedAudio.Bandwidth {
			selectedAudio = &audioStreams[i]
		}
	}

	if selectedVideo == nil || selectedAudio == nil {
		fmt.Fprintln(os.Stderr, "Failed to select video/audio streams")
		os.Exit(1)
	}

	fmt.Printf("Video: %s %s (%s, %d segs)\n",
		selectedVideo.Name, selectedVideo.Resolution,
		selectedVideo.FormatBandwidth(), selectedVideo.SegmentsCount)
	fmt.Printf("Audio: %s %s (%s, %d segs)\n",
		selectedAudio.Name, selectedAudio.Language,
		selectedAudio.FormatBandwidth(), selectedAudio.SegmentsCount)

	// Progress bar
	var lastProgressTime time.Time
	handler := m3u8dl.EventHandlerFunc{
		OnProgressFn: func(e m3u8dl.ProgressEvent) {
			now := time.Now()
			if now.Sub(lastProgressTime) < 200*time.Millisecond {
				return
			}
			lastProgressTime = now
			printProgressBar(e.Percent, e.SegmentsDone, e.Segments, e.Speed, e.ETA)
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			fmt.Fprintf(os.Stderr, "\n[%s] %s\n", e.Status, e.TaskID)
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				fmt.Fprintf(os.Stderr, "\n[%s] %s\n", logLevelStr(e.Level), e.Message)
			}
		},
	}

	// Download video segments only (no merge)
	fmt.Fprintf(os.Stderr, "\nDownloading video stream...\n")
	videoReq := model.DownloadRequest{
		Stream:             selectedVideo,
		URL:                url,
		OutputDir:          outputDir,
		SaveName:           saveName + "_video",
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		DelAfterDone:       false,
	}
	videoResult, err := engine.DownloadOnly(ctx, videoReq, handler)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nVideo download failed: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(videoResult.TempDir)
	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))

	// Download audio segments only (no merge)
	fmt.Fprintf(os.Stderr, "Downloading audio stream...\n")
	audioReq := model.DownloadRequest{
		Stream:             selectedAudio,
		URL:                url,
		OutputDir:          outputDir,
		SaveName:           saveName + "_audio",
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		DelAfterDone:       false,
	}
	audioResult, err := engine.DownloadOnly(ctx, audioReq, handler)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nAudio download failed: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(audioResult.TempDir)
	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))

	// Mux according to user's merge mode
	outputPath := filepath.Join(outputDir, saveName+".mp4")
	fmt.Fprintf(os.Stderr, "Muxing %d video + %d audio segments → %s (%s)\n",
		len(videoResult.SegmentPaths), len(audioResult.SegmentPaths),
		outputPath, mergeModeStr(mode))

	isTSSegments := isTSFormat(videoResult.SegmentPaths)

	var muxErr error
	switch mode {
	case model.MergeModeTS2MP4:
		if isTSSegments {
			// TS → 解析PES → 直接输出fMP4 (纯Go)
			muxErr = merge.MuxSeparateTSStreams(
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			// fMP4 → 合并moov+moof → 输出fMP4 (纯Go)
			muxErr = merge.MuxFMP4FromSegments(
				videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}

	case model.MergeModeFMP4:
		// fMP4 → 合并moov+moof → 输出fMP4 (纯Go)
		muxErr = merge.MuxFMP4FromSegments(
			videoResult.InitPath, audioResult.InitPath,
			videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)

	case model.MergeModeFFmpeg:
		// 用ffmpeg混流
		if isTSSegments {
			// TS先各自合并再ffmpeg混流
			videoMerged := filepath.Join(videoResult.TempDir, "video_merged.ts")
			audioMerged := filepath.Join(audioResult.TempDir, "audio_merged.ts")
			merge.BinaryMerge(videoResult.SegmentPaths, videoMerged)
			merge.BinaryMerge(audioResult.SegmentPaths, audioMerged)
			muxErr = merge.FFmpegMuxAV(videoMerged, audioMerged, outputPath, "ffmpeg")
		} else {
			videoMerged := filepath.Join(videoResult.TempDir, "video_merged.mp4")
			audioMerged := filepath.Join(audioResult.TempDir, "audio_merged.mp4")
			merge.BinaryMerge(videoResult.SegmentPaths, videoMerged)
			merge.BinaryMerge(audioResult.SegmentPaths, audioMerged)
			muxErr = merge.FFmpegMuxAV(videoMerged, audioMerged, outputPath, "ffmpeg")
		}

	default: // binary
		if isTSSegments {
			// TS → 直接转fMP4 (纯Go, binary模式下也输出mp4因为分离流无法用纯ts)
			muxErr = merge.MuxSeparateTSStreams(
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			muxErr = merge.MuxFMP4FromSegments(
				videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}
	}

	if muxErr != nil {
		fmt.Fprintf(os.Stderr, "\nMux failed: %v\n", muxErr)
		os.Exit(1)
	}

	fmt.Printf("Done! Output: %s\n", outputPath)
}

// downloadSingleStream downloads a single stream (existing behavior).
func downloadSingleStream(ctx context.Context, engine *m3u8dl.Engine, url string,
	selected *model.StreamInfo, outputDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	autoSub, subOnly bool) {

	fmt.Printf("Selected: %s %s (%s, %d segments)\n",
		selected.Name, selected.Resolution,
		selected.FormatBandwidth(), selected.SegmentsCount)

	var lastProgressTime time.Time
	req := model.DownloadRequest{
		Stream:             selected,
		URL:                url,
		OutputDir:          outputDir,
		SaveName:           saveName,
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		MergeMode:          mode,
		AutoSubtitleFix:    autoSub,
		SubOnly:            subOnly,
		DelAfterDone:       true,
	}

	handler := m3u8dl.EventHandlerFunc{
		OnProgressFn: func(e m3u8dl.ProgressEvent) {
			now := time.Now()
			if now.Sub(lastProgressTime) < 200*time.Millisecond {
				return
			}
			lastProgressTime = now
			printProgressBar(e.Percent, e.SegmentsDone, e.Segments, e.Speed, e.ETA)
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			fmt.Fprintf(os.Stderr, "\n[%s] %s\n", e.Status, e.TaskID)
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				fmt.Fprintf(os.Stderr, "\n[%s] %s\n", logLevelStr(e.Level), e.Message)
			}
		},
	}

	if err := engine.Download(ctx, req, handler); err != nil {
		fmt.Fprintf(os.Stderr, "\nDownload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
	outputPath := buildOutputPath(outputDir, saveName, mode)
	fmt.Printf("Done! Output: %s\n", outputPath)
}

// printProgressBar renders a single-line progress bar.
func printProgressBar(pct float64, done, total int, speed int64, eta float64) {
	barWidth := 30
	filled := int(pct / 100 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	speedStr := formatBytes(speed)
	etaStr := formatETA(eta)

	fmt.Fprintf(os.Stderr, "\r  %s %5.1f%% %d/%d %s/s ETA %s   ",
		bar, pct, done, total, speedStr, etaStr)
}

func formatETA(seconds float64) string {
	if seconds <= 0 || seconds > 36000 {
		return "--:--"
	}
	d := time.Duration(seconds * float64(time.Second))
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m >= 60 {
		h := m / 60
		m = m % 60
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func mergeModeStr(m model.MergeMode) string {
	switch m {
	case model.MergeModeBinary:
		return "binary"
	case model.MergeModeTS2MP4:
		return "ts2mp4"
	case model.MergeModeFMP4:
		return "fmp4"
	case model.MergeModeFFmpeg:
		return "ffmpeg"
	default:
		return "unknown"
	}
}

// isTSFormat checks if segment files are MPEG-TS format by reading the first bytes.
func isTSFormat(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	f, err := os.Open(paths[0])
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 16)
	n, err := f.Read(buf)
	if err != nil || n < 4 {
		return false
	}

	// TS files start with sync byte 0x47, typically at offset 0
	if buf[0] == 0x47 {
		return true
	}
	// Some TS files have garbage before the first sync byte
	for i := 0; i < n; i++ {
		if buf[i] == 0x47 {
			return true
		}
	}

	return false
}

// generateSaveName creates a filename using date+timestamp format.
func generateSaveName(url string, stream *model.StreamInfo) string {
	now := time.Now()
	return now.Format("20060102") + "+" + strconv.FormatInt(now.Unix(), 10)
}

// svFilter holds parsed -sv selection criteria.
type svFilter struct {
	idRegex      *regexp.Regexp
	langRegex    *regexp.Regexp
	nameRegex    *regexp.Regexp
	codecsRegex  *regexp.Regexp
	resRegex     *regexp.Regexp
	frameRegex   *regexp.Regexp
	segsMin      int
	segsMax      int
	chRegex      *regexp.Regexp
	rangeRegex   *regexp.Regexp
	urlRegex     *regexp.Regexp
	plistDurMin  time.Duration
	plistDurMax  time.Duration
	bwMin        int
	bwMax        int
	role         string
	forMode      string // best[n], worst[n], all
}

// parseSVFilter parses a colon-separated -sv filter string.
// Format: key=value:key=value:...
func parseSVFilter(raw string) (*svFilter, error) {
	f := &svFilter{forMode: "best"}

	parts := strings.Split(raw, ":")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid -sv token: %q (expected key=value)", part)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		// Strip surrounding quotes if present
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}

		switch strings.ToLower(key) {
		case "id":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid id regex %q: %w", val, err)
			}
			f.idRegex = rx
		case "lang", "language":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid lang regex %q: %w", val, err)
			}
			f.langRegex = rx
		case "name":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid name regex %q: %w", val, err)
			}
			f.nameRegex = rx
		case "codecs":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid codecs regex %q: %w", val, err)
			}
			f.codecsRegex = rx
		case "res", "resolution":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid res regex %q: %w", val, err)
			}
			f.resRegex = rx
		case "frame", "framerate":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid frame regex %q: %w", val, err)
			}
			f.frameRegex = rx
		case "segsmin":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid segsMin %q: %w", val, err)
			}
			f.segsMin = n
		case "segsmax":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid segsMax %q: %w", val, err)
			}
			f.segsMax = n
		case "ch", "channels":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid ch regex %q: %w", val, err)
			}
			f.chRegex = rx
		case "range":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid range regex %q: %w", val, err)
			}
			f.rangeRegex = rx
		case "url":
			rx, err := regexp.Compile(val)
			if err != nil {
				return nil, fmt.Errorf("invalid url regex %q: %w", val, err)
			}
			f.urlRegex = rx
		case "pldurmin", "plistdurmin":
			d, err := parseHMSDuration(val)
			if err != nil {
				return nil, fmt.Errorf("invalid plistDurMin %q: %w", val, err)
			}
			f.plistDurMin = d
		case "pldurmax", "plistdurmax":
			d, err := parseHMSDuration(val)
			if err != nil {
				return nil, fmt.Errorf("invalid plistDurMax %q: %w", val, err)
			}
			f.plistDurMax = d
		case "bwmin":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid bwMin %q: %w", val, err)
			}
			f.bwMin = n
		case "bwmax":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid bwMax %q: %w", val, err)
			}
			f.bwMax = n
		case "role":
			f.role = val
		case "for":
			f.forMode = strings.ToLower(val)
		default:
			return nil, fmt.Errorf("unknown -sv key: %q", key)
		}
	}

	return f, nil
}

// parseHMSDuration parses a duration string like "1h20m30s" or "45s" or "2m".
func parseHMSDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	// Try Go's built-in duration parser first (handles "1h20m30s")
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Try plain seconds
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("cannot parse duration: %q", s)
}

// streamMatches checks if a stream passes all criteria in the filter.
func streamMatches(s *model.StreamInfo, f *svFilter) bool {
	if f.idRegex != nil && !f.idRegex.MatchString(s.GroupID) {
		return false
	}
	if f.langRegex != nil && !f.langRegex.MatchString(s.Language) {
		return false
	}
	if f.nameRegex != nil && !f.nameRegex.MatchString(s.Name) {
		return false
	}
	if f.codecsRegex != nil && !f.codecsRegex.MatchString(s.Codecs) {
		return false
	}
	if f.resRegex != nil && !f.resRegex.MatchString(s.Resolution) {
		return false
	}
	if f.frameRegex != nil {
		frameStr := fmt.Sprintf("%.2f", s.FrameRate)
		if !f.frameRegex.MatchString(frameStr) {
			return false
		}
	}
	if f.segsMin > 0 && s.SegmentsCount < f.segsMin {
		return false
	}
	if f.segsMax > 0 && s.SegmentsCount > f.segsMax {
		return false
	}
	if f.chRegex != nil && !f.chRegex.MatchString(s.Channels) {
		return false
	}
	if f.rangeRegex != nil && !f.rangeRegex.MatchString(s.VideoRange) {
		return false
	}
	if f.urlRegex != nil && !f.urlRegex.MatchString(s.URL) {
		return false
	}
	if f.plistDurMin > 0 || f.plistDurMax > 0 {
		dur := calcPlaylistDuration(s)
		if f.plistDurMin > 0 && dur < f.plistDurMin {
			return false
		}
		if f.plistDurMax > 0 && dur > f.plistDurMax {
			return false
		}
	}
	if f.bwMin > 0 && s.Bandwidth < f.bwMin {
		return false
	}
	if f.bwMax > 0 && s.Bandwidth > f.bwMax {
		return false
	}
	if f.role != "" && !strings.EqualFold(s.Role, f.role) {
		return false
	}
	return true
}

// calcPlaylistDuration returns the total duration of a stream's playlist.
func calcPlaylistDuration(s *model.StreamInfo) time.Duration {
	if s.Playlist == nil {
		return 0
	}
	if s.Playlist.TotalDuration > 0 {
		return time.Duration(s.Playlist.TotalDuration * float64(time.Second))
	}
	var total float64
	for _, part := range s.Playlist.MediaParts {
		for _, seg := range part.MediaSegments {
			total += seg.Duration
		}
	}
	return time.Duration(total * float64(time.Second))
}

// selectStreamByFilter applies a -sv filter and selects the matching stream.
func selectStreamByFilter(streams []model.StreamInfo, svRaw string) *model.StreamInfo {
	f, err := parseSVFilter(svRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing -sv: %v\n", err)
		os.Exit(1)
	}

	// Filter matching streams
	type scored struct {
		stream *model.StreamInfo
		idx    int
	}
	var matches []scored
	for i := range streams {
		if streamMatches(&streams[i], f) {
			matches = append(matches, scored{stream: &streams[i], idx: i})
		}
	}

	if len(matches) == 0 {
		fmt.Fprintln(os.Stderr, "No streams match -sv filter")
		os.Exit(1)
	}

	// Sort by bandwidth descending for best/worst logic
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].stream.Bandwidth > matches[j].stream.Bandwidth
	})

	switch {
	case f.forMode == "all":
		// Print all matches and let user pick
		fmt.Println("\nMatched streams:")
		for i, m := range matches {
			fmt.Printf("  [%d] %-12s %-16s %s (%d segs)\n",
				i+1, m.stream.Name, m.stream.Resolution,
				m.stream.FormatBandwidth(), m.stream.SegmentsCount)
		}
		fmt.Printf("Select [1-%d] (default: 1): ", len(matches))
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		choice := 1
		if input != "" {
			if n, err := fmt.Sscanf(input, "%d", &choice); n != 1 || err != nil || choice < 1 || choice > len(matches) {
				fmt.Fprintf(os.Stderr, "Invalid choice: %s\n", input)
				os.Exit(1)
			}
		}
		return matches[choice-1].stream

	case strings.HasPrefix(f.forMode, "worst"):
		n := parseForCount(f.forMode)
		if n >= len(matches) {
			return matches[len(matches)-1].stream
		}
		return matches[len(matches)-n].stream

	default: // "best" or "bestN"
		n := parseForCount(f.forMode)
		if n >= len(matches) {
			return matches[0].stream
		}
		return matches[n-1].stream
	}
}

// parseForCount extracts N from "bestN" or "worstN", defaults to 1.
func parseForCount(mode string) int {
	mode = strings.TrimPrefix(mode, "best")
	mode = strings.TrimPrefix(mode, "worst")
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return 1
	}
	n, err := strconv.Atoi(mode)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func buildOutputPath(dir, name string, mode model.MergeMode) string {
	if dir == "" {
		dir = "."
	}
	switch mode {
	case model.MergeModeBinary:
		return dir + "/" + name + ".ts"
	default:
		return dir + "/" + name + ".mp4"
	}
}

// logLevelStr returns a string representation of a log level.
func logLevelStr(level m3u8dl.LogLevel) string {
	switch level {
	case m3u8dl.LogDebug:
		return "DEBUG"
	case m3u8dl.LogInfo:
		return "INFO"
	case m3u8dl.LogWarn:
		return "WARN"
	case m3u8dl.LogError:
		return "ERROR"
	default:
		return "?"
	}
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB"}
	val := float64(b)
	for _, u := range units {
		if val < 1024 {
			return fmt.Sprintf("%.1f %s", val, u)
		}
		val /= 1024
	}
	return fmt.Sprintf("%.1f TB", val)
}

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}
