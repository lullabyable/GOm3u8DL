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
		return nil, fmt.Errorf("获取播放列表失败: %w", err)
	}

	format := detectFormat(body, url)

	switch format {
	case "hls":
		ext := hls.NewExtractor(url)
		streams, playlist, err := ext.Parse(body)
		if err != nil {
			return nil, fmt.Errorf("解析 HLS 失败: %w", err)
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
			return nil, fmt.Errorf("解析 DASH 失败: %w", err)
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
			return nil, fmt.Errorf("解析 MSS 失败: %w", err)
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
		return nil, fmt.Errorf("未知的播放列表格式")
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
			return fmt.Errorf("未提供流或 URL")
		}
		emitLog(LogInfo, fmt.Sprintf("正在解析: %s", req.URL))
		streams, err := e.GetStreams(ctx, req.URL, req.Headers)
		if err != nil {
			return fmt.Errorf("获取流失败: %w", err)
		}
		if len(streams) == 0 {
			return fmt.Errorf("未找到流")
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
		return fmt.Errorf("流无播放列表 (分段未解析)")
	}

	// Count total segments
	var allSegments []model.MediaSegment
	for _, part := range stream.Playlist.MediaParts {
		allSegments = append(allSegments, part.MediaSegments...)
	}
	if len(allSegments) == 0 {
		return fmt.Errorf("播放列表中无分段")
	}

	emitLog(LogInfo, fmt.Sprintf("流: %s %s (%d 个分段)",
		stream.Name, stream.Resolution, len(allSegments)))

	// 2. Fetch encryption keys if needed
	if err := e.fetchEncryptionKeys(ctx, stream.Playlist, req.Headers); err != nil {
		emitStatus(model.TaskStatusFailed)
		return fmt.Errorf("获取解密密钥失败: %w", err)
	}

	// 3. Create output and temp directories
	outDir := req.OutputDir
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	// Temp dir: if TmpDir is set, use it directly (caller controls structure).
	// Otherwise default to {outputDir}/{saveName}_tmp.
	tempDir := req.TmpDir
	if tempDir == "" {
		if req.SaveName != "" {
			tempDir = filepath.Join(outDir, req.SaveName+"_tmp")
		} else {
			tempDir = filepath.Join(outDir, "download_tmp")
		}
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	if req.DelAfterDone && req.MergeMode != model.MergeModeNo {
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
		return fmt.Errorf("下载分段失败: %w", err)
	}

	// 4. Merge segments (skip if MergeModeNo)
	outputPath := buildOutputPath(req)

	// Auto-detect segment format and adjust merge mode if needed
	mergeMode := req.MergeMode
	if mergeMode != model.MergeModeNo && mergeMode != model.MergeModeFFmpeg && len(segmentPaths) > 0 {
		detected := detectSegmentFormat(segmentPaths[0])
		if detected == "fmp4" && mergeMode == model.MergeModeTS2MP4 {
			emitLog(LogInfo, "检测到 fMP4 分段, 切换到 fmp4 合并模式")
			mergeMode = model.MergeModeFMP4
		} else if detected == "ts" && mergeMode == model.MergeModeFMP4 {
			emitLog(LogInfo, "检测到 TS 分段, 切换到 ts2mp4 合并模式")
			mergeMode = model.MergeModeTS2MP4
		}
	}

	if mergeMode == model.MergeModeNo {
		emitLog(LogInfo, fmt.Sprintf("仅下载模式 — %d 个分段保存在: %s", len(segmentPaths), tempDir))
	} else {
		emitStatus(model.TaskStatusMerging)
		emitLog(LogInfo, fmt.Sprintf("正在合并 %d 个分段 (%s)...", len(segmentPaths), mergeModeStr(mergeMode)))

		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("创建输出目录失败: %w", err)
		}

		switch mergeMode {
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
			emitLog(LogInfo, "使用 TS→MP4 重封装 (纯 Go)")
			err = merge.TS2MP4Remux(segmentPaths, outputPath)
		case model.MergeModeFMP4:
			emitLog(LogInfo, "使用分片 MP4 合并 (纯 Go)")
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
			emitLog(LogInfo, fmt.Sprintf("使用 ffmpeg 合并 (%s)", ffmpegPath))
			err = merge.FFmpegMerge(segmentPaths, outputPath, ffmpegPath)
		default:
			err = merge.BinaryMerge(segmentPaths, outputPath)
		}

		if err != nil {
			emitStatus(model.TaskStatusFailed)
			return fmt.Errorf("合并失败: %w", err)
		}
	}

	// 5. Final report
	duration := time.Since(startTime).Seconds()
	emitStatus(model.TaskStatusDone)
	emitLog(LogInfo, fmt.Sprintf("完成! 输出: %s (%.1fs)", outputPath, duration))

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
			return nil, fmt.Errorf("未提供流或 URL")
		}
		streams, err := e.GetStreams(ctx, req.URL, req.Headers)
		if err != nil {
			return nil, fmt.Errorf("获取流失败: %w", err)
		}
		if len(streams) == 0 {
			return nil, fmt.Errorf("未找到流")
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
		return nil, fmt.Errorf("流无播放列表")
	}

	// Fetch encryption keys
	if err := e.fetchEncryptionKeys(ctx, stream.Playlist, req.Headers); err != nil {
		return nil, fmt.Errorf("获取解密密钥失败: %w", err)
	}

	// Create output and temp directories
	outDir := req.OutputDir
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}
	// Temp dir: if TmpDir is set, use it directly (caller controls structure).
	// Otherwise default to {outputDir}/{saveName}_tmp.
	tempDir := req.TmpDir
	if tempDir == "" {
		if req.SaveName != "" {
			tempDir = filepath.Join(outDir, req.SaveName+"_tmp")
		} else {
			tempDir = filepath.Join(outDir, "download_tmp")
		}
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
		return nil, fmt.Errorf("下载分段失败: %w", err)
	}

	// Find init segment path if exists
	initPath := ""
	if stream.Playlist.MediaInit != nil {
		p := filepath.Join(tempDir, "seg_-1.ts")
		if _, err := os.Stat(p); err == nil {
			initPath = p
		}
	}

	emitLog(LogInfo, fmt.Sprintf("已下载 %d 个分段到 %s", len(segmentPaths), tempDir))

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
		return fmt.Errorf("未找到流")
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
	case model.MergeModeNo:
		// No merge — return temp dir as output reference
		if req.TmpDir != "" {
			return req.TmpDir
		}
		return filepath.Join(dir, name+"_tmp")
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
	case model.MergeModeNo:
		return "no (download only)"
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
			return fmt.Errorf("获取密钥 %s 失败: %w", url, err)
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
		return nil, fmt.Errorf("无效的 AES-128 密钥长度: %d 字节 (预期 16)", len(key))
	}

	return key, nil
}

// detectSegmentFormat reads the first bytes of a file to determine if it's
// TS (MPEG-TS sync byte 0x47) or fMP4 (ftyp/moof/moov box).
func detectSegmentFormat(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()

	buf := make([]byte, 32)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return "unknown"
	}

	// TS: sync byte 0x47 at offset 0
	if buf[0] == 0x47 {
		return "ts"
	}

	// fMP4: check for ftyp/moof/moov box type at offset 4
	if n >= 8 {
		boxType := string(buf[4:8])
		switch boxType {
		case "ftyp", "moof", "moov", "styp", "sidx":
			return "fmp4"
		}
	}

	// Scan for TS sync byte in case of leading garbage
	for i := 0; i < n && i < 188*3; i++ {
		if buf[i] == 0x47 {
			// Check if next sync byte is 188 bytes later
			if i+188 < n && buf[i+188] == 0x47 {
				return "ts"
			}
		}
	}

	return "unknown"
}
