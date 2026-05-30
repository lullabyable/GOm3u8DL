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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

var (
	version = "dev"
	commit  = "none"
)

// ANSI color codes
const (
	cReset   = "\033[0m"
	cBold    = "\033[1m"
	cDim     = "\033[2m"
	cRed     = "\033[31m"
	cGreen   = "\033[32m"
	cYellow  = "\033[33m"
	cBlue    = "\033[34m"
	cMagenta = "\033[35m"
	cCyan    = "\033[36m"
	cWhite   = "\033[37m"
	cBgBlack  = "\033[40m"
	cBgGreen  = "\033[42m"
	cBgYellow = "\033[43m"
	cBgRed    = "\033[41m"
	cBgCyan   = "\033[46m"
	cBgMagenta = "\033[45m"

	// Cursor control
	cursorUp    = "\033[%dA"
	cursorDown  = "\033[%dB"
	eraseLine   = "\033[2K"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
	saveCursor  = "\033[s"
	restCursor  = "\033[u"
)

// progressLines tracks how many lines the progress display uses
// so we know how many lines to move the cursor up for the next refresh.
var progressLines int32

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
		printBanner()
		fmt.Printf("  Version : %s (%s)\n", version, commit)
		fmt.Printf("  Runtime : %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		fmt.Println()
		os.Exit(0)
	}

	// URL can also be a positional argument
	if url == "" && flag.NArg() > 0 {
		url = flag.Arg(0)
	}

	// ── Interactive mode (double-click launch) ──────────────────────
	if url == "" && !hasStdinData() {
		url, outputDir, saveName, concurrency, maxSpeed, mergeMode, headers, keys, autoSub, subOnly, svSelect = interactiveMode()
		if url == "" {
			fmt.Fprintf(os.Stderr, "%sError: URL is required%s\n", cRed, cReset)
			os.Exit(1)
		}
	}

	if url == "" {
		printBanner()
		fmt.Fprintf(os.Stderr, "%sError: URL is required%s\n", cRed, cReset)
		fmt.Fprintln(os.Stderr, "Usage: m3u8dl -url <URL> [options]")
		fmt.Fprintln(os.Stderr, "       m3u8dl <URL> [options]")
		fmt.Fprintln(os.Stderr, "       m3u8dl  (interactive mode)")
		fmt.Fprintln(os.Stderr)
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
	mode := parseMergeMode(mergeMode)

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
		fmt.Fprintf(os.Stderr, "\n%s  CANCEL  %s Interrupted, cancelling...%s\n", cBgRed+cWhite, cRed, cReset)
		cancel()
	}()

	// Print banner and task info
	printBanner()
	printTaskInfo(url, outputDir, saveName, mergeMode, concurrency, maxSpeed, headerMap)

	// Get streams
	fmt.Printf("\n%s  PARSING  %s Fetching manifest...%s\n", cBgCyan+cWhite, cCyan, cReset)
	streams, err := engine.GetStreams(ctx, url, headerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s %v%s\n", cBgRed+cWhite, cRed, err, cReset)
		os.Exit(1)
	}

	if len(streams) == 0 {
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s No streams found%s\n", cBgRed+cWhite, cRed, cReset)
		os.Exit(1)
	}

	// Auto-generate save name if not provided
	if saveName == "" {
		saveName = generateSaveName(url, nil)
	}

	// Separate streams by media type
	var videoStreams, audioStreams []model.StreamInfo
	for _, s := range streams {
		switch s.MediaType {
		case model.MediaTypeAudio:
			audioStreams = append(audioStreams, s)
		default:
			videoStreams = append(videoStreams, s)
		}
	}

	hasSeparateAV := len(videoStreams) > 0 && len(audioStreams) > 0

	if hasSeparateAV {
		downloadSeparateStreams(ctx, engine, url, videoStreams, audioStreams,
			svSelect, outputDir, saveName, headerMap, concurrency, maxSpeed,
			mode, autoSub, subOnly)
	} else {
		// Select stream
		var selected *model.StreamInfo
		if svSelect != "" {
			selected = selectStreamByFilter(streams, svSelect)
		} else if len(streams) == 1 {
			selected = &streams[0]
		} else {
			selected = interactiveStreamSelect(streams)
		}

		if selected == nil {
			fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s No stream selected%s\n", cBgRed+cWhite, cRed, cReset)
			os.Exit(1)
		}

		printStreamInfo(selected)
		downloadSingleStream(ctx, engine, url, selected, outputDir, saveName,
			headerMap, concurrency, maxSpeed, mode, autoSub, subOnly)
	}
}

// ── Interactive Mode ──────────────────────────────────────────────────

// hasStdinData checks if stdin has piped data.
func hasStdinData() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// interactiveMode prompts the user for all parameters step by step.
func interactiveMode() (url, outputDir, saveName string, concurrency int, maxSpeed int64,
	mergeMode string, headers stringSlice, keys stringSlice, autoSub, subOnly bool, svSelect string) {

	reader := bufio.NewReader(os.Stdin)
	mergeMode = "ts2mp4"

	printBanner()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════╗%s\n", cCyan, cReset)
	fmt.Printf("%s║%s  Interactive Mode — press Enter to accept defaults          %s║%s\n", cCyan, cWhite, cCyan, cReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════╝%s\n\n", cCyan, cReset)

	// URL (required)
	for {
		fmt.Printf("  %s▶%s URL %s(required)%s: ", cGreen, cWhite, cYellow, cReset)
		url, _ = reader.ReadString('\n')
		url = strings.TrimSpace(url)
		if url != "" {
			break
		}
		fmt.Printf("    %s⚠ URL cannot be empty%s\n\n", cRed, cReset)
	}

	// Output directory
	fmt.Printf("  %s▶%s Output dir %s[default: /downloads]%s: ", cGreen, cWhite, cDim, cReset)
	outputDir, _ = reader.ReadString('\n')
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		outputDir = "/downloads"
	}

	// Save name
	fmt.Printf("  %s▶%s Save name %s[default: auto-generated]%s: ", cGreen, cWhite, cDim, cReset)
	saveName, _ = reader.ReadString('\n')
	saveName = strings.TrimSpace(saveName)

	// Concurrency
	fmt.Printf("  %s▶%s Concurrency %s[default: 8]%s: ", cGreen, cWhite, cDim, cReset)
	concStr, _ := reader.ReadString('\n')
	concStr = strings.TrimSpace(concStr)
	concurrency = 8
	if concStr != "" {
		if n, err := strconv.Atoi(concStr); err == nil && n > 0 {
			concurrency = n
		}
	}

	// Max speed
	fmt.Printf("  %s▶%s Max speed %s[default: unlimited, e.g. 2M, 500K]%s: ", cGreen, cWhite, cDim, cReset)
	speedStr, _ := reader.ReadString('\n')
	speedStr = strings.TrimSpace(speedStr)
	maxSpeed = 0
	if speedStr != "" {
		maxSpeed = parseSpeed(speedStr)
	}

	// Merge mode
	fmt.Printf("  %s▶%s Merge mode %s[binary|ts2mp4|fmp4|ffmpeg, default: ts2mp4]%s: ", cGreen, cWhite, cDim, cReset)
	mergeInput, _ := reader.ReadString('\n')
	mergeInput = strings.TrimSpace(mergeInput)
	if mergeInput != "" {
		mergeMode = mergeInput
	}

	// Headers
	fmt.Printf("  %s▶%s HTTP Headers %s[format: Key: Value, empty to skip]%s:\n", cGreen, cWhite, cDim, cReset)
	for {
		fmt.Printf("    %s>%s ", cDim, cReset)
		h, _ := reader.ReadString('\n')
		h = strings.TrimSpace(h)
		if h == "" {
			break
		}
		headers = append(headers, h)
	}

	// Keys
	fmt.Printf("  %s▶%s Decryption keys %s[format: kid:key hex, empty to skip]%s:\n", cGreen, cWhite, cDim, cReset)
	for {
		fmt.Printf("    %s>%s ", cDim, cReset)
		k, _ := reader.ReadString('\n')
		k = strings.TrimSpace(k)
		if k == "" {
			break
		}
		keys = append(keys, k)
	}

	// Stream selection filter
	fmt.Printf("  %s▶%s Stream filter %s[-sv filter, e.g. best, empty for interactive]%s: ", cGreen, cWhite, cDim, cReset)
	svSelect, _ = reader.ReadString('\n')
	svSelect = strings.TrimSpace(svSelect)

	fmt.Println()
	return
}

// parseSpeed parses a speed string like "2M", "500K", "1048576" into bytes/sec.
func parseSpeed(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}

	multiplier := int64(1)
	if strings.HasSuffix(s, "G") {
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "M") {
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "K") {
		multiplier = 1024
		s = s[:len(s)-1]
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(val * float64(multiplier))
}

// ── Banner & Info Printing ────────────────────────────────────────────

func printBanner() {
	fmt.Printf("\n")
	fmt.Printf("  %s%s╔═══════════════════════════════════════════════════════╗%s\n", cCyan, cBold, cReset)
	fmt.Printf("  %s%s║         GOm3u8DL — Stream Downloader                 ║%s\n", cCyan, cBold, cReset)
	fmt.Printf("  %s%s║         Pure Go HLS / DASH / MSS                     ║%s\n", cCyan, cBold, cReset)
	fmt.Printf("  %s%s╚═══════════════════════════════════════════════════════╝%s\n", cCyan, cBold, cReset)
	fmt.Printf("\n")
}

func printTaskInfo(url, outputDir, saveName, mergeMode string, concurrency int, maxSpeed int64, headers map[string]string) {
	fmt.Printf("  %s┌─ Task Info ─────────────────────────────────────────────┐%s\n", cDim, cReset)
	fmt.Printf("  %s│%s URL         : %s%s%s\n", cDim, cReset, cWhite, truncateURL(url, 80), cReset)
	fmt.Printf("  %s│%s Output Dir  : %s%s%s\n", cDim, cReset, cWhite, outputDir, cReset)
	if saveName != "" {
		fmt.Printf("  %s│%s Save Name   : %s%s%s\n", cDim, cReset, cWhite, saveName, cReset)
	}
	fmt.Printf("  %s│%s Concurrency : %s%d%s\n", cDim, cReset, cWhite, concurrency, cReset)
	if maxSpeed > 0 {
		fmt.Printf("  %s│%s Max Speed   : %s%s/s%s\n", cDim, cReset, cWhite, formatBytes(maxSpeed), cReset)
	} else {
		fmt.Printf("  %s│%s Max Speed   : %sunlimited%s\n", cDim, cReset, cWhite, cReset)
	}
	fmt.Printf("  %s│%s Merge Mode  : %s%s%s\n", cDim, cReset, cWhite, mergeMode, cReset)
	if len(headers) > 0 {
		for k, v := range headers {
			fmt.Printf("  %s│%s Header      : %s%s: %s%s\n", cDim, cReset, cCyan, k, v, cReset)
		}
	}
	fmt.Printf("  %s└────────────────────────────────────────────────────────┘%s\n", cDim, cReset)
}

func printStreamInfo(s *model.StreamInfo) {
	fmt.Printf("\n  %s┌─ Selected Stream ───────────────────────────────────────┐%s\n", cGreen, cReset)
	fmt.Printf("  %s│%s Name       : %s%s%s\n", cGreen, cReset, cWhite, s.Name, cReset)
	if s.Resolution != "" {
		fmt.Printf("  %s│%s Resolution : %s%s%s\n", cGreen, cReset, cWhite, s.Resolution, cReset)
	}
	if s.Codecs != "" {
		fmt.Printf("  %s│%s Codecs     : %s%s%s\n", cGreen, cReset, cWhite, s.Codecs, cReset)
	}
	if s.Bandwidth > 0 {
		fmt.Printf("  %s│%s Bandwidth  : %s%s%s\n", cGreen, cReset, cWhite, s.FormatBandwidth(), cReset)
	}
	if s.Language != "" {
		fmt.Printf("  %s│%s Language   : %s%s%s\n", cGreen, cReset, cWhite, s.Language, cReset)
	}
	if s.VideoRange != "" && s.VideoRange != "SDR" {
		fmt.Printf("  %s│%s HDR        : %s%s%s\n", cGreen, cReset, cYellow, s.VideoRange, cReset)
	}
	fmt.Printf("  %s│%s Segments   : %s%d%s\n", cGreen, cReset, cWhite, s.SegmentsCount, cReset)
	fmt.Printf("  %s└────────────────────────────────────────────────────────┘%s\n", cGreen, cReset)
}

// ── Interactive Stream Selection ──────────────────────────────────────

func interactiveStreamSelect(streams []model.StreamInfo) *model.StreamInfo {
	if len(streams) == 0 {
		return nil
	}
	if len(streams) == 1 {
		return &streams[0]
	}

	fmt.Printf("\n  %s┌─ Available Streams ───────────────────────────────────────────────────────────┐%s\n", cYellow, cReset)

	// Header
	fmt.Printf("  %s│%s  %s%-4s %-12s %-20s %-14s %-10s %-8s %-6s%s  %s│%s\n",
		cYellow, cReset,
		cBold, "#", "Name", "Resolution", "Bandwidth", "Codecs", "Lang", "Segs",
		cReset, cYellow, cReset)

	fmt.Printf("  %s│%s%s%s│%s\n", cYellow, cReset,
		strings.Repeat("─", 78), cYellow, cReset)

	for i, s := range streams {
		lang := s.Language
		if lang == "" {
			lang = "-"
		}
		res := s.Resolution
		if res == "" {
			res = "-"
		}
		codecs := s.Codecs
		if codecs == "" {
			codecs = "-"
		}
		if utf8.RuneCountInString(codecs) > 12 {
			runes := []rune(codecs)
			if len(runes) > 12 {
				codecs = string(runes[:12]) + "…"
			}
		}
		bw := "-"
		if s.Bandwidth > 0 {
			bw = s.FormatBandwidth()
		}

		// Highlight video streams
		nameColor := cWhite
		if s.MediaType == model.MediaTypeAudio {
			nameColor = cBlue
		} else if s.MediaType == model.MediaTypeSubtitles {
			nameColor = cMagenta
		}

		fmt.Printf("  %s│%s  %s%-4d%s %s%-12s%s %-20s %-14s %-10s %-8s %-6d  %s│%s\n",
			cYellow, cReset,
			cCyan, i+1, cReset,
			nameColor, s.Name, cReset,
			res, bw, codecs, lang, s.SegmentsCount,
			cYellow, cReset)
	}

	fmt.Printf("  %s└─────────────────────────────────────────────────────────────────────────────────┘%s\n", cYellow, cReset)

	// Prompt
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\n  %s▶%s Select stream %s[1-%d, default: 1]%s: ", cGreen, cWhite, cDim, len(streams), cReset)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		choice := 1
		if input != "" {
			n, err := strconv.Atoi(input)
			if err != nil || n < 1 || n > len(streams) {
				fmt.Printf("    %s⚠ Invalid choice: %s%s\n", cRed, input, cReset)
				continue
			}
			choice = n
		}

		return &streams[choice-1]
	}
}

// ── Rich Progress Display ─────────────────────────────────────────────

// printProgress renders a multi-line progress display (similar to N_m3u8DL-RE).
// It uses ANSI escape codes to overwrite previous output.
func printProgress(e m3u8dl.ProgressEvent, status string) {
	// Move cursor up to overwrite previous progress block
	lines := int(atomic.LoadInt32(&progressLines))
	if lines > 0 {
		fmt.Fprintf(os.Stderr, "\033[%dA", lines)
	}

	barWidth := 40
	filled := int(e.Percent / 100 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	// Build the progress bar with gradient colors
	bar := ""
	for i := 0; i < barWidth; i++ {
		if i < filled {
			if i < barWidth/3 {
				bar += cRed + "━" + cReset
			} else if i < barWidth*2/3 {
				bar += cYellow + "━" + cReset
			} else {
				bar += cGreen + "━" + cReset
			}
		} else {
			bar += cDim + "─" + cReset
		}
	}

	speedStr := formatBytesSpeed(e.Speed)
	avgStr := formatBytesSpeed(e.AvgSpeed)
	downStr := formatBytes(e.Downloaded)
	totalStr := formatBytes(e.Total)
	etaStr := formatETA(e.ETA)
	elapsedStr := formatDuration(e.Elapsed)

	// Status indicator
	statusIcon := cGreen + "▶" + cReset
	if status == "downloading" {
		statusIcon = cYellow + "↓" + cReset
	} else if status == "merging" {
		statusIcon = cBlue + "⟳" + cReset
	} else if status == "done" {
		statusIcon = cGreen + "✓" + cReset
	}

	var linesOut []string

	// Line 1: Progress bar + percentage
	linesOut = append(linesOut, fmt.Sprintf("  %s %s %s%5.1f%%  %s %d/%d segments",
		statusIcon, bar, cBold+cWhite, e.Percent, cReset,
		e.SegmentsDone, e.Segments))

	// Line 2: Stats
	linesOut = append(linesOut, fmt.Sprintf("       %sSize:%s %-10s  %sSpeed:%s %-11s  %sAvg:%s %-11s  %sETA:%s %s  %sElapsed:%s %s",
		cDim, cReset, downStr+"/"+totalStr,
		cGreen, cReset, speedStr,
		cDim, cReset, avgStr,
		cYellow, cReset, etaStr,
		cDim, cReset, elapsedStr))

	// Print all lines
	for _, l := range linesOut {
		fmt.Fprintf(os.Stderr, "%s%s\n", eraseLine, l)
	}

	atomic.StoreInt32(&progressLines, int32(len(linesOut)))
}

// printDone displays the final completion message with stats.
func printDone(outputPath string, elapsed float64, fileSize int64) {
	lines := int(atomic.LoadInt32(&progressLines))
	if lines > 0 {
		fmt.Fprintf(os.Stderr, "\033[%dA", lines)
		for i := 0; i < lines; i++ {
			fmt.Fprintf(os.Stderr, "%s\n", eraseLine)
		}
		fmt.Fprintf(os.Stderr, "\033[%dA", lines)
	}
	atomic.StoreInt32(&progressLines, 0)

	fmt.Printf("\n")
	fmt.Printf("  %s%s  DONE  ✓%s %sDownload complete!%s\n", cBgGreen, cWhite+cBold, cReset, cGreen, cReset)
	fmt.Printf("\n")
	fmt.Printf("  %s┌─ Result ────────────────────────────────────────────────┐%s\n", cGreen, cReset)
	fmt.Printf("  %s│%s Output   : %s%s%s\n", cGreen, cReset, cWhite, outputPath, cReset)
	fmt.Printf("  %s│%s Size     : %s%s%s\n", cGreen, cReset, cWhite, formatBytes(fileSize), cReset)
	fmt.Printf("  %s│%s Duration : %s%s%s\n", cGreen, cReset, cWhite, formatDuration(elapsed), cReset)
	if elapsed > 0 && fileSize > 0 {
		avgSpeed := float64(fileSize) / elapsed
		fmt.Printf("  %s│%s Avg Speed: %s%s/s%s\n", cGreen, cReset, cWhite, formatBytes(int64(avgSpeed)), cReset)
	}
	fmt.Printf("  %s└────────────────────────────────────────────────────────┘%s\n\n", cGreen, cReset)
}

// resetProgress clears the progress display area.
func resetProgress() {
	lines := int(atomic.LoadInt32(&progressLines))
	if lines > 0 {
		for i := 0; i < lines; i++ {
			fmt.Fprintf(os.Stderr, "%s\n", eraseLine)
		}
		fmt.Fprintf(os.Stderr, "\033[%dA", lines)
	}
	atomic.StoreInt32(&progressLines, 0)
}

// ── Download Functions ────────────────────────────────────────────────

func downloadSeparateStreams(ctx context.Context, engine *m3u8dl.Engine, url string,
	videoStreams, audioStreams []model.StreamInfo, svSelect, outputDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	autoSub, subOnly bool) {

	// Select best video
	var selectedVideo *model.StreamInfo
	if svSelect != "" {
		selectedVideo = selectStreamByFilter(videoStreams, svSelect)
	} else if len(videoStreams) == 1 {
		selectedVideo = &videoStreams[0]
	} else {
		selectedVideo = interactiveStreamSelect(videoStreams)
	}

	// Select best audio
	var selectedAudio *model.StreamInfo
	if len(audioStreams) == 1 {
		selectedAudio = &audioStreams[0]
	} else {
		selectedAudio = interactiveStreamSelect(audioStreams)
	}

	if selectedVideo == nil || selectedAudio == nil {
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s Failed to select video/audio streams%s\n", cBgRed+cWhite, cRed, cReset)
		os.Exit(1)
	}

	fmt.Printf("\n  %s┌─ Dual Stream ───────────────────────────────────────────┐%s\n", cMagenta, cReset)
	fmt.Printf("  %s│%s Video : %s%s %s (%s, %d segs)%s\n",
		cMagenta, cReset, cWhite, selectedVideo.Name, selectedVideo.Resolution,
		selectedVideo.FormatBandwidth(), selectedVideo.SegmentsCount, cReset)
	fmt.Printf("  %s│%s Audio : %s%s %s (%s, %d segs)%s\n",
		cMagenta, cReset, cWhite, selectedAudio.Name, selectedAudio.Language,
		selectedAudio.FormatBandwidth(), selectedAudio.SegmentsCount, cReset)
	fmt.Printf("  %s└────────────────────────────────────────────────────────┘%s\n", cMagenta, cReset)

	var lastProgressTime time.Time
	handler := m3u8dl.EventHandlerFunc{
		OnProgressFn: func(e m3u8dl.ProgressEvent) {
			now := time.Now()
			if now.Sub(lastProgressTime) < 200*time.Millisecond {
				return
			}
			lastProgressTime = now
			printProgress(e, "downloading")
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			resetProgress()
			fmt.Printf("  %s  %-12s  %s%s%s\n", cBgCyan+cWhite, strings.ToUpper(e.Status.String()), cCyan, e.TaskID, cReset)
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				resetProgress()
				fmt.Fprintf(os.Stderr, "  %s[%-5s]%s %s\n", cYellow, logLevelStr(e.Level), cReset, e.Message)
			}
		},
	}

	// Download video
	fmt.Printf("\n%s  VIDEO  %s Downloading video stream...%s\n", cBgMagenta+cWhite, cMagenta, cReset)
	atomic.StoreInt32(&progressLines, 0)
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
		resetProgress()
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s Video download failed: %v%s\n", cBgRed+cWhite, cRed, err, cReset)
		os.Exit(1)
	}
	defer os.RemoveAll(videoResult.TempDir)
	resetProgress()

	// Download audio
	fmt.Printf("\n%s  AUDIO  %s Downloading audio stream...%s\n", cBgMagenta+cWhite, cMagenta, cReset)
	atomic.StoreInt32(&progressLines, 0)
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
		resetProgress()
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s Audio download failed: %v%s\n", cBgRed+cWhite, cRed, err, cReset)
		os.Exit(1)
	}
	defer os.RemoveAll(audioResult.TempDir)
	resetProgress()

	// Mux
	outputPath := filepath.Join(outputDir, saveName+".mp4")
	fmt.Printf("\n%s  MUX  %s Muxing %d video + %d audio → %s (%s)%s\n",
		cBgYellow+cWhite, cYellow,
		len(videoResult.SegmentPaths), len(audioResult.SegmentPaths),
		outputPath, mergeModeStr(mode), cReset)

	isTSSegments := isTSFormat(videoResult.SegmentPaths)

	var muxErr error
	switch mode {
	case model.MergeModeTS2MP4:
		if isTSSegments {
			muxErr = merge.MuxSeparateTSStreams(
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			muxErr = merge.MuxFMP4FromSegments(
				videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}
	case model.MergeModeFMP4:
		muxErr = merge.MuxFMP4FromSegments(
			videoResult.InitPath, audioResult.InitPath,
			videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
	case model.MergeModeFFmpeg:
		if isTSSegments {
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
	default:
		if isTSSegments {
			muxErr = merge.MuxSeparateTSStreams(
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			muxErr = merge.MuxFMP4FromSegments(
				videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}
	}

	if muxErr != nil {
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s Mux failed: %v%s\n", cBgRed+cWhite, cRed, muxErr, cReset)
		os.Exit(1)
	}

	printDone(outputPath, 0, 0)
}

func downloadSingleStream(ctx context.Context, engine *m3u8dl.Engine, url string,
	selected *model.StreamInfo, outputDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	autoSub, subOnly bool) {

	startTime := time.Now()
	atomic.StoreInt32(&progressLines, 0)

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
			printProgress(e, "downloading")
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			resetProgress()
			statusStr := strings.ToUpper(e.Status.String())
			icon := cYellow + "⟳" + cReset
			if e.Status == model.TaskStatusDone {
				icon = cGreen + "✓" + cReset
			} else if e.Status == model.TaskStatusFailed {
				icon = cRed + "✗" + cReset
			} else if e.Status == model.TaskStatusMerging {
				icon = cBlue + "⟳" + cReset
			}
			fmt.Printf("  %s  %-12s  %s%s%s\n", icon, statusStr, cDim, e.TaskID, cReset)
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				resetProgress()
				fmt.Fprintf(os.Stderr, "  %s[%-5s]%s %s\n", cYellow, logLevelStr(e.Level), cReset, e.Message)
			}
		},
	}

	if err := engine.Download(ctx, req, handler); err != nil {
		resetProgress()
		fmt.Fprintf(os.Stderr, "\n%s  ERROR  %s Download failed: %v%s\n", cBgRed+cWhite, cRed, err, cReset)
		os.Exit(1)
	}

	elapsed := time.Since(startTime).Seconds()
	outputPath := buildOutputPath(outputDir, saveName, mode)

	// Get file size
	var fileSize int64
	if fi, err := os.Stat(outputPath); err == nil {
		fileSize = fi.Size()
	}

	printDone(outputPath, elapsed, fileSize)
}

// ── Utilities ─────────────────────────────────────────────────────────

func formatETA(seconds float64) string {
	if seconds <= 0 || seconds > 36000 {
		return "--:--"
	}
	d := time.Duration(seconds * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "0s"
	}
	d := time.Duration(seconds * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func formatBytesSpeed(b int64) string {
	if b <= 0 {
		return "0 B/s"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	val := float64(b)
	for _, u := range units {
		if val < 1024 {
			return fmt.Sprintf("%.1f %s", val, u)
		}
		val /= 1024
	}
	return fmt.Sprintf("%.1f TB/s", val)
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

func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen-3] + "..."
}

func generateSaveName(url string, stream *model.StreamInfo) string {
	now := time.Now()
	return now.Format("20060102") + "+" + strconv.FormatInt(now.Unix(), 10)
}

func parseMergeMode(s string) model.MergeMode {
	switch strings.ToLower(s) {
	case "binary":
		return model.MergeModeBinary
	case "ts2mp4":
		return model.MergeModeTS2MP4
	case "fmp4":
		return model.MergeModeFMP4
	case "ffmpeg":
		return model.MergeModeFFmpeg
	default:
		fmt.Fprintf(os.Stderr, "Unknown merge mode: %s, using ts2mp4\n", s)
		return model.MergeModeTS2MP4
	}
}

func buildOutputPath(dir, name string, mode model.MergeMode) string {
	if dir == "" {
		dir = "."
	}
	switch mode {
	case model.MergeModeBinary:
		return filepath.Join(dir, name+".ts")
	default:
		return filepath.Join(dir, name+".mp4")
	}
}

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

// isTSFormat checks if segment files are MPEG-TS format.
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

	if buf[0] == 0x47 {
		return true
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0x47 {
			return true
		}
	}

	return false
}

// ── Stream Filter (unchanged from original) ───────────────────────────

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
	forMode      string
}

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

func parseHMSDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("cannot parse duration: %q", s)
}

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

func selectStreamByFilter(streams []model.StreamInfo, svRaw string) *model.StreamInfo {
	f, err := parseSVFilter(svRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing -sv: %v\n", err)
		os.Exit(1)
	}

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

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].stream.Bandwidth > matches[j].stream.Bandwidth
	})

	switch {
	case f.forMode == "all":
		// Use interactive selection for "all" mode
		var matchedStreams []model.StreamInfo
		for _, m := range matches {
			matchedStreams = append(matchedStreams, *m.stream)
		}
		return interactiveStreamSelect(matchedStreams)

	case strings.HasPrefix(f.forMode, "worst"):
		n := parseForCount(f.forMode)
		if n >= len(matches) {
			return matches[len(matches)-1].stream
		}
		return matches[len(matches)-n].stream

	default:
		n := parseForCount(f.forMode)
		if n >= len(matches) {
			return matches[0].stream
		}
		return matches[n-1].stream
	}
}

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

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}
