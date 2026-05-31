package m3u8dl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/downloader"
	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
	"github.com/lullabyable/GOm3u8DL/pkg/parser/dash"
	"github.com/lullabyable/GOm3u8DL/pkg/parser/hls"
	"github.com/lullabyable/GOm3u8DL/pkg/parser/mss"
)

// Engine is the main entry point for the download engine.
type Engine struct {
	opts   Options
	client *http.Client
}

// New creates a new Engine with the given options.
func New(opts ...Option) *Engine {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return &Engine{
		opts: o,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 16,
			},
		},
	}
}

// fetchURL performs an HTTP GET and returns the response body as a string.
func (e *Engine) fetchURL(ctx context.Context, url string, headers map[string]string) (string, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("http status %d for %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read body: %w", err)
	}

	return string(body), resp.Header, nil
}

// detectFormat determines the manifest format from content or URL.
func detectFormat(content, url string) string {
	lower := strings.TrimSpace(content)
	lowerURL := strings.ToLower(url)

	// Check content first
	if strings.HasPrefix(lower, "#extm3u") {
		return "hls"
	}
	if strings.Contains(lower, "<mpd") || strings.Contains(lower, "urn:mpeg:dash:schema:mpd") {
		return "dash"
	}
	if strings.Contains(lower, "<smoothstreamingmedia") {
		return "mss"
	}

	// Fallback to URL extension
	if strings.Contains(lowerURL, ".m3u8") || strings.Contains(lowerURL, ".m3u") {
		return "hls"
	}
	if strings.Contains(lowerURL, ".mpd") {
		return "dash"
	}
	if strings.Contains(lowerURL, ".ism") || strings.Contains(lowerURL, "manifest") {
		return "mss"
	}

	// Default to HLS
	return "hls"
}

// GetStreams parses a URL and returns available streams.
// The consumer selects a stream, then calls Download.
func (e *Engine) GetStreams(ctx context.Context, url string, headers map[string]string) ([]model.StreamInfo, error) {
	body, _, err := e.fetchURL(ctx, url, headers)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	format := detectFormat(body, url)

	switch format {
	case "hls":
		ext := hls.NewExtractor(url)
		streams, playlist, err := ext.Parse(body)
		if err != nil {
			return nil, fmt.Errorf("parse HLS: %w", err)
		}
		// If it's a media playlist (no master), wrap as a single stream
		if len(streams) == 0 && playlist != nil {
			streams = []model.StreamInfo{
				{
					MediaType:  model.MediaTypeVideo,
					URL:        url,
					Playlist:   playlist,
					Name:       "default",
					Resolution: "unknown",
				},
			}
		}
		// For master playlists, fetch sub-playlists to get segment info
		for i := range streams {
			if streams[i].Playlist == nil && streams[i].URL != "" {
				subBody, _, err := e.fetchURL(ctx, streams[i].URL, headers)
				if err != nil {
					continue
				}
				_, subPlaylist, err := ext.Parse(subBody)
				if err == nil && subPlaylist != nil {
					streams[i].Playlist = subPlaylist
					streams[i].SegmentsCount = 0
					for _, part := range subPlaylist.MediaParts {
						streams[i].SegmentsCount += len(part.MediaSegments)
					}
				}
			}
		}
		return streams, nil

	case "dash":
		ext := dash.NewExtractor(url)
		streams, err := ext.Parse(body)
		if err != nil {
			return nil, fmt.Errorf("parse DASH: %w", err)
		}
		for i := range streams {
			if streams[i].Playlist != nil {
				for _, part := range streams[i].Playlist.MediaParts {
					streams[i].SegmentsCount += len(part.MediaSegments)
				}
			}
		}
		return streams, nil

	case "mss":
		ext := mss.NewExtractor(url)
		streams, err := ext.Parse(body)
		if err != nil {
			return nil, fmt.Errorf("parse MSS: %w", err)
		}
		for i := range streams {
			if streams[i].Playlist != nil {
				for _, part := range streams[i].Playlist.MediaParts {
					streams[i].SegmentsCount += len(part.MediaSegments)
				}
			}
		}
		return streams, nil

	default:
		return nil, fmt.Errorf("unknown manifest format")
	}
}

// Download downloads the specified stream.
// Events are delivered via the handler callback.
func (e *Engine) Download(ctx context.Context, req model.DownloadRequest, handler EventHandler) error {
	startTime := time.Now()

	// Helper to emit events
	emitLog := func(level LogLevel, msg string) {
		if handler != nil {
			handler.OnLog(LogEvent{Level: level, Message: msg})
		}
	}
	emitStatus := func(status model.TaskStatus) {
		if handler != nil {
			handler.OnStatusChange(StatusEvent{TaskID: req.SaveName, Status: status})
		}
	}

	emitStatus(model.TaskStatusParsing)

	// 1. If no Stream provided, parse the URL
	stream := req.Stream
	if stream == nil {
		if req.URL == "" {
			return fmt.Errorf("no stream or URL provided")
		}
		emitLog(LogInfo, fmt.Sprintf("Parsing: %s", req.URL))
		streams, err := e.GetStreams(ctx, req.URL, req.Headers)
		if err != nil {
			return fmt.Errorf("get streams: %w", err)
		}
		if len(streams) == 0 {
			return fmt.Errorf("no streams found")
		}
		// Auto-select: pick highest bandwidth video stream
		stream = &streams[0]
		for i := range streams {
			if streams[i].MediaType == model.MediaTypeVideo &&
				streams[i].Bandwidth > stream.Bandwidth {
				stream = &streams[i]
			}
		}
	}

	if stream.Playlist == nil {
		return fmt.Errorf("stream has no playlist (segments not resolved)")
	}

	// Count total segments
	var allSegments []model.MediaSegment
	for _, part := range stream.Playlist.MediaParts {
		allSegments = append(allSegments, part.MediaSegments...)
	}
	if len(allSegments) == 0 {
		return fmt.Errorf("no segments in playlist")
	}

	emitLog(LogInfo, fmt.Sprintf("Stream: %s %s (%d segments)",
		stream.Name, stream.Resolution, len(allSegments)))

	// 2. Fetch encryption keys if needed
	if err := e.fetchEncryptionKeys(ctx, stream.Playlist, req.Headers); err != nil {
		emitStatus(model.TaskStatusFailed)
		return fmt.Errorf("fetch encryption keys: %w", err)
	}

	// 3. Create output and temp directories
	outDir := req.OutputDir
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	// Temp dir: use explicit TmpDir if set, otherwise {outputDir}/{saveName}_tmp
	// Always create a subdirectory based on SaveName to keep video/audio separate.
	tempDir := req.TmpDir
	if tempDir == "" {
		tempDir = outDir
	}
	if req.SaveName != "" {
		tempDir = filepath.Join(tempDir, req.SaveName+"_tmp")
	} else {
		tempDir = filepath.Join(tempDir, "download_tmp")
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	if req.DelAfterDone {
		defer func() { _ = downloader.CleanupTemp(tempDir) }()
	}

	// 3. Download segments using Manager
	emitStatus(model.TaskStatusDownloading)

	concurrency := req.ThreadCount
	if concurrency <= 0 {
		concurrency = e.opts.SegmentConcurrency
	}
	retries := req.DownloadRetryCount
	if retries <= 0 {
		retries = 3
	}

	mgr := downloader.NewManager(
		downloader.WithConcurrency(concurrency),
		downloader.WithRetries(retries),
		downloader.WithHTTPClient(e.client),
		downloader.WithProgressFunc(func(p downloader.Progress) {
			if handler != nil {
				handler.OnProgress(ProgressEvent{
					TaskID:       req.SaveName,
					Total:        p.Total,
					Downloaded:   p.Downloaded,
					Speed:        p.Speed,
					AvgSpeed:     p.AvgSpeed,
					Segments:     p.Segments,
					SegmentsDone: p.SegmentsDone,
					Percent:      p.Percent,
					ETA:          p.ETA,
					Elapsed:      p.Elapsed,
				})
			}
		}),
	)

	segmentPaths, err := mgr.DownloadSegments(ctx, stream.Playlist, tempDir)
	if err != nil {
		emitStatus(model.TaskStatusFailed)
		return fmt.Errorf("download segments: %w", err)
	}

	// 4. Merge segments
	emitStatus(model.TaskStatusMerging)
	emitLog(LogInfo, fmt.Sprintf("Merging %d segments (%s)...", len(segmentPaths), mergeModeStr(req.MergeMode)))

	outputPath := buildOutputPath(req)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	switch req.MergeMode {
	case model.MergeModeBinary:
		// Prepend init segment if present
		if stream.Playlist.MediaInit != nil {
			initPath := downloader.SegmentPath(tempDir, -1)
			if _, statErr := os.Stat(initPath); statErr == nil {
				err = merge.BinaryMergeWithInit(initPath, segmentPaths, outputPath)
			} else {
				err = merge.BinaryMerge(segmentPaths, outputPath)
			}
		} else {
			err = merge.BinaryMerge(segmentPaths, outputPath)
		}
	case model.MergeModeTS2MP4:
		emitLog(LogInfo, "Using TS→MP4 remux (pure Go)")
		err = merge.TS2MP4Remux(segmentPaths, outputPath)
	case model.MergeModeFMP4:
		emitLog(LogInfo, "Using fragmented MP4 merge (pure Go)")
		// FMP4Merge needs init segment path as first arg
		initPath := ""
		if stream.Playlist.MediaInit != nil {
			initPath = filepath.Join(tempDir, "seg_-1.ts")
		}
		err = merge.FMP4Merge(initPath, segmentPaths, outputPath)
	case model.MergeModeFFmpeg:
		ffmpegPath := req.FFmpegPath
		if ffmpegPath == "" {
			ffmpegPath = "ffmpeg"
		}
		emitLog(LogInfo, fmt.Sprintf("Using ffmpeg merge (%s)", ffmpegPath))
		err = merge.FFmpegMerge(segmentPaths, outputPath, ffmpegPath)
	default:
		err = merge.BinaryMerge(segmentPaths, outputPath)
	}

	if err != nil {
		emitStatus(model.TaskStatusFailed)
		return fmt.Errorf("merge: %w", err)
	}

	// 5. Final report
	duration := time.Since(startTime).Seconds()
	emitStatus(model.TaskStatusDone)
	emitLog(LogInfo, fmt.Sprintf("Done! Output: %s (%.1fs)", outputPath, duration))

	if handler != nil {
		handler.OnProgress(ProgressEvent{
			TaskID:       req.SaveName,
			Segments:     len(allSegments),
			SegmentsDone: len(allSegments),
			Percent:      100,
		})
	}

	return nil
}

// DownloadResult holds the result of a DownloadOnly call.
type DownloadResult struct {
	SegmentPaths []string // paths to downloaded segment files
	InitPath     string   // path to init segment (empty if none)
	TempDir      string   // temp directory (caller must clean up)
	Playlist     *model.Playlist
}

// DownloadOnly downloads segments without merging. Returns segment paths
// and init segment path for the caller to handle merging/muxing.
// Caller is responsible for cleaning up TempDir.
func (e *Engine) DownloadOnly(ctx context.Context, req model.DownloadRequest, handler EventHandler) (*DownloadResult, error) {
	emitLog := func(level LogLevel, msg string) {
		if handler != nil {
			handler.OnLog(LogEvent{Level: level, Message: msg})
		}
	}

	// Resolve stream
	stream := req.Stream
	if stream == nil {
		if req.URL == "" {
			return nil, fmt.Errorf("no stream or URL provided")
		}
		streams, err := e.GetStreams(ctx, req.URL, req.Headers)
		if err != nil {
			return nil, fmt.Errorf("get streams: %w", err)
		}
		if len(streams) == 0 {
			return nil, fmt.Errorf("no streams found")
		}
		stream = &streams[0]
		for i := range streams {
			if streams[i].MediaType == model.MediaTypeVideo &&
				streams[i].Bandwidth > stream.Bandwidth {
				stream = &streams[i]
			}
		}
	}

	if stream.Playlist == nil {
		return nil, fmt.Errorf("stream has no playlist")
	}

	// Fetch encryption keys
	if err := e.fetchEncryptionKeys(ctx, stream.Playlist, req.Headers); err != nil {
		return nil, fmt.Errorf("fetch encryption keys: %w", err)
	}

	// Create output and temp directories
	outDir := req.OutputDir
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	// Temp dir: use explicit TmpDir if set, otherwise {outputDir}/{saveName}_tmp
	// Always create a subdirectory based on SaveName to keep video/audio separate.
	tempDir := req.TmpDir
	if tempDir == "" {
		tempDir = outDir
	}
	if req.SaveName != "" {
		tempDir = filepath.Join(tempDir, req.SaveName+"_tmp")
	} else {
		tempDir = filepath.Join(tempDir, "download_tmp")
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	// Download segments
	concurrency := req.ThreadCount
	if concurrency <= 0 {
		concurrency = e.opts.SegmentConcurrency
	}
	retries := req.DownloadRetryCount
	if retries <= 0 {
		retries = 3
	}

	mgr := downloader.NewManager(
		downloader.WithConcurrency(concurrency),
		downloader.WithRetries(retries),
		downloader.WithHTTPClient(e.client),
		downloader.WithProgressFunc(func(p downloader.Progress) {
			if handler != nil {
				handler.OnProgress(ProgressEvent{
					TaskID:       req.SaveName,
					Total:        p.Total,
					Downloaded:   p.Downloaded,
					Speed:        p.Speed,
					AvgSpeed:     p.AvgSpeed,
					Segments:     p.Segments,
					SegmentsDone: p.SegmentsDone,
					Percent:      p.Percent,
					ETA:          p.ETA,
					Elapsed:      p.Elapsed,
				})
			}
		}),
	)

	segmentPaths, err := mgr.DownloadSegments(ctx, stream.Playlist, tempDir)
	if err != nil {
		return nil, fmt.Errorf("download segments: %w", err)
	}

	// Find init segment path if exists
	initPath := ""
	if stream.Playlist.MediaInit != nil {
		p := filepath.Join(tempDir, "seg_-1.ts")
		if _, err := os.Stat(p); err == nil {
			initPath = p
		}
	}

	emitLog(LogInfo, fmt.Sprintf("Downloaded %d segments to %s", len(segmentPaths), tempDir))

	return &DownloadResult{
		SegmentPaths: segmentPaths,
		InitPath:     initPath,
		TempDir:      tempDir,
		Playlist:     stream.Playlist,
	}, nil
}

// DownloadWithAutoSelect auto-selects the best stream and downloads it.
func (e *Engine) DownloadWithAutoSelect(ctx context.Context, url string, handler EventHandler) error {
	streams, err := e.GetStreams(ctx, url, nil)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return fmt.Errorf("no streams found")
	}

	// Apply AutoSelectRule, for now pick first video stream
	best := &streams[0]
	for i := range streams {
		if streams[i].MediaType == model.MediaTypeVideo &&
			streams[i].Bandwidth > best.Bandwidth {
			best = &streams[i]
		}
	}

	return e.Download(ctx, model.DownloadRequest{
		Stream: best,
	}, handler)
}

// buildOutputPath constructs the final output file path.
func buildOutputPath(req model.DownloadRequest) string {
	dir := req.OutputDir
	if dir == "" {
		dir = "."
	}
	name := req.SaveName
	if name == "" {
		name = "output"
	}
	// Add extension based on merge mode
	switch req.MergeMode {
	case model.MergeModeBinary:
		return filepath.Join(dir, name+".ts")
	case model.MergeModeTS2MP4, model.MergeModeFMP4:
		return filepath.Join(dir, name+".mp4")
	case model.MergeModeFFmpeg:
		return filepath.Join(dir, name+".mp4")
	default:
		return filepath.Join(dir, name+".ts")
	}
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

// fetchEncryptionKeys fetches encryption keys for all segments in the playlist.
// It collects unique key URLs, fetches each key once, and sets the Key bytes
// on all segments that reference that URL.
func (e *Engine) fetchEncryptionKeys(ctx context.Context, playlist *model.Playlist, headers map[string]string) error {
	if playlist == nil {
		return nil
	}

	// Collect unique key URLs
	type keyInfo struct {
		url    string
		needed bool
	}
	keyMap := make(map[string]*keyInfo) // keyURL -> info

	for _, part := range playlist.MediaParts {
		for _, seg := range part.MediaSegments {
			if seg.EncryptInfo.Method != model.EncryptMethodNone && seg.EncryptInfo.KeyURL != "" {
				if _, exists := keyMap[seg.EncryptInfo.KeyURL]; !exists {
					keyMap[seg.EncryptInfo.KeyURL] = &keyInfo{url: seg.EncryptInfo.KeyURL}
				}
			}
		}
	}

	if len(keyMap) == 0 {
		return nil
	}

	// Fetch each unique key
	fetchedKeys := make(map[string][]byte)
	for url, info := range keyMap {
		keyBytes, err := e.fetchKey(ctx, info.url, headers)
		if err != nil {
			return fmt.Errorf("fetch key %s: %w", url, err)
		}
		fetchedKeys[url] = keyBytes
	}

	// Set key bytes on all segments
	for i := range playlist.MediaParts {
		for j := range playlist.MediaParts[i].MediaSegments {
			seg := &playlist.MediaParts[i].MediaSegments[j]
			if seg.EncryptInfo.KeyURL != "" {
				if key, ok := fetchedKeys[seg.EncryptInfo.KeyURL]; ok {
					seg.EncryptInfo.Key = key
				}
			}
		}
	}

	return nil
}

// fetchKey downloads an encryption key from the given URL.
func (e *Engine) fetchKey(ctx context.Context, keyURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keyURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d for key %s", resp.StatusCode, keyURL)
	}

	key, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read key body: %w", err)
	}

	if len(key) != 16 {
		return nil, fmt.Errorf("invalid AES-128 key length: %d bytes (expected 16)", len(key))
	}

	return key, nil
}
