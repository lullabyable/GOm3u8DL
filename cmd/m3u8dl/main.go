package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

var (
	version = "1.0.0"
	commit  = "none"
)

// ── ANSI helpers ──────────────────────────────────────────────────────

const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
	grey    = "\033[90m"

	bgGreen  = "\033[42m"
	bgRed    = "\033[41m"
	bgYellow = "\033[43m"
	bgCyan   = "\033[46m"
	bgGrey   = "\033[100m"

	eraseLine = "\033[2K"
	hideCur   = "\033[?25l"
	showCur   = "\033[?25h"
)

// spinner frames
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var spinnerIdx int

func nextSpinner() string {
	spinnerIdx++
	return spinnerFrames[spinnerIdx%len(spinnerFrames)]
}

func main() {
	var (
		url         string
		outputDir   string
		tmpDir      string
		saveName    string
		concurrency int
		maxSpeed    int64
		mergeMode   string
		ffmpegDir   string
		headers     stringSlice
		keys        stringSlice
		autoSub     bool
		subOnly     bool
		showVersion bool
		svSelect    string
	)

	flag.StringVar(&url, "url", "", "M3U8/MPD/ISM URL (required)")
	flag.StringVar(&outputDir, "save-dir", "./downloads", "Output directory")
	flag.StringVar(&tmpDir, "tmp-dir", "", "Temp directory for downloads (default: {save-dir}/)")
	flag.StringVar(&saveName, "save-name", "", "Output filename (without extension)")
	flag.IntVar(&concurrency, "thread-num", 8, "Segment download concurrency")
	flag.Int64Var(&maxSpeed, "max-speed", 0, "Max download speed in bytes/sec (0=unlimited)")
	flag.StringVar(&mergeMode, "merge", "ts2mp4", "Merge mode: binary, ts2mp4, fmp4, ffmpeg, no")
	flag.StringVar(&ffmpegDir, "ffmpeg-dir", "", "Path to ffmpeg binary or directory")
	flag.Var(&headers, "H", "HTTP header (repeatable, format: Key: Value)")
	flag.Var(&keys, "key", "Decryption key in kid:key hex format (repeatable)")
	flag.BoolVar(&autoSub, "auto-subtitle-fix", false, "Auto-fix subtitle timing")
	flag.BoolVar(&subOnly, "sub-only", false, "Download subtitles only")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.StringVar(&svSelect, "sv", "", "Stream selection filter")

	// Pre-process args: if first arg is a URL (not a flag), insert -url flag
	// so that flags after the URL (e.g. "m3u8dl <URL> -save-name foo") are parsed.
	args := os.Args[1:]
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		// First arg is not a flag — treat as URL, insert -url prefix
		newArgs := make([]string, 0, len(args)+2)
		newArgs = append(newArgs, "-url", args[0])
		newArgs = append(newArgs, args[1:]...)
		os.Args = append([]string{os.Args[0]}, newArgs...)
	}

	flag.Parse()

	if showVersion {
		fmt.Printf("GOm3u8DL %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Track which flags were explicitly set via CLI (for config conflict resolution)
	cliFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		cliFlags[f.Name] = true
	})

	// URL already handled by pre-processing above

	// ── Interactive mode (one-shot) ─────────────────────────────────
	if url == "" && !hasStdinPiped() {
		url, outputDir, tmpDir, saveName, concurrency, maxSpeed, mergeMode, ffmpegDir, headers, keys, autoSub, subOnly, svSelect = interactiveMode()
		if url == "" {
			fmt.Fprintf(os.Stderr, "%sError: URL is required%s\n", red, reset)
			os.Exit(1)
		}
	}

	if url == "" {
		fmt.Fprintf(os.Stderr, "GOm3u8DL %s (%s)\n\n", version, commit)
		fmt.Fprintln(os.Stderr, "Usage: m3u8dl -url <URL> [options]")
		fmt.Fprintln(os.Stderr, "       m3u8dl <URL> [options]")
		fmt.Fprintln(os.Stderr, "       m3u8dl  (one-shot interactive: <URL> [flags])")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
		os.Exit(1)
	}

	// ── Load config file (CLI flags take priority) ──────────────────
	if cfgPath, found := m3u8dl.FindConfig(); found {
		cfg, err := m3u8dl.LoadConfig(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s[warn]%s Config load failed (%s): %v\n", yellow, reset, cfgPath, err)
		} else {
			fmt.Printf("%s[info]%s Config loaded: %s\n", cyan, reset, cfgPath)
			// Apply config defaults — only for flags NOT explicitly set via CLI
			if !cliFlags["thread-num"] && cfg.ThreadNum > 0 {
				concurrency = cfg.ThreadNum
			}
			if !cliFlags["max-speed"] && cfg.MaxSpeed > 0 {
				maxSpeed = cfg.MaxSpeed
			}
			if !cliFlags["save-dir"] && cfg.SaveDir != "" && cfg.SaveDir != "./downloads" {
				outputDir = cfg.SaveDir
			}
			if !cliFlags["merge"] && cfg.Merge != "" {
				mergeMode = cfg.Merge
			}
			if !cliFlags["ffmpeg-dir"] && cfg.FFmpegDir != "" {
				ffmpegDir = cfg.FFmpegDir
			}
			if !cliFlags["auto-subtitle-fix"] && cfg.AutoSubtitleFix {
				autoSub = true
			}
			if !cliFlags["tmp-dir"] && cfg.TmpDir != "" {
				tmpDir = cfg.TmpDir
			}
			// Merge headers: config provides base, CLI overrides same keys.
			// Always merge (not gated on len(cfg.Headers)) so CLI headers
			// are never lost even when config has no headers.
			if len(cfg.Headers) > 0 || len(headers) > 0 {
				merged := make(map[string]string)
				// 1. Config headers as base
				for k, v := range cfg.Headers {
					merged[k] = v
				}
				// 2. CLI headers override
				for _, h := range headers {
					parts := strings.SplitN(h, ":", 2)
					if len(parts) == 2 {
						merged[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
					}
				}
				// 3. Rebuild headers slice from merged map
				headers = make([]string, 0, len(merged))
				for k, v := range merged {
					headers = append(headers, k+": "+v)
				}
			}
		}
	}

	// Parse headers
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	mode := parseMergeMode(mergeMode)

	// Validate ffmpeg availability if merge mode is ffmpeg
	var ffmpegPath string
	if mode == model.MergeModeFFmpeg {
		ffmpegPath = findFFmpeg(ffmpegDir)
		fmt.Printf("%s[info]%s Using ffmpeg: %s\n", cyan, reset, ffmpegPath)
	}

	engine := m3u8dl.New(
		m3u8dl.WithSegmentConcurrency(concurrency),
		m3u8dl.WithGlobalMaxSpeed(maxSpeed),
		m3u8dl.WithLogLevel(m3u8dl.LogInfo),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Print(showCur) // restore cursor on exit
		fmt.Fprintf(os.Stderr, "\n%s[warn]%s User cancelled.\n", yellow, reset)
		cancel()
		os.Exit(0)
	}()

	// ── Banner ──────────────────────────────────────────────────────
	printBanner(url)

	// ── Parse manifest ──────────────────────────────────────────────
	fmt.Printf("%s[info]%s Fetching manifest...\n", cyan, reset)
	streams, err := engine.GetStreams(ctx, url, headerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s[error]%s %v\n", red, reset, err)
		os.Exit(1)
	}
	if len(streams) == 0 {
		fmt.Fprintf(os.Stderr, "%s[error]%s No streams found\n", red, reset)
		os.Exit(1)
	}

	if saveName == "" {
		saveName = generateSaveName(url)
	}

	// ── Print all streams ───────────────────────────────────────────
	fmt.Printf("\n%s[info]%s %d streams found:\n", cyan, reset, len(streams))
	for _, s := range streams {
		fmt.Println("  " + streamToString(s))
	}

	// ── Separate by type ────────────────────────────────────────────
	var videoStreams, audioStreams []model.StreamInfo
	for _, s := range streams {
		if s.MediaType == model.MediaTypeAudio {
			audioStreams = append(audioStreams, s)
		} else {
			videoStreams = append(videoStreams, s)
		}
	}
	hasSeparateAV := len(videoStreams) > 0 && len(audioStreams) > 0

	if hasSeparateAV {
		downloadSeparateStreams(ctx, engine, url, videoStreams, audioStreams,
			svSelect, outputDir, tmpDir, saveName, headerMap, concurrency, maxSpeed, mode, ffmpegPath, autoSub, subOnly)
	} else {
		var selected *model.StreamInfo
		if svSelect != "" {
			selected = selectStreamByFilter(streams, svSelect)
		} else if len(streams) == 1 {
			selected = &streams[0]
		} else {
			selected = interactiveStreamSelect(streams)
		}
		if selected == nil {
			fmt.Fprintf(os.Stderr, "%s[error]%s No stream selected\n", red, reset)
			os.Exit(1)
		}

		fmt.Printf("\n%s[info]%s Selected: %s\n", cyan, reset, streamToShortString(*selected))
		downloadSingleStream(ctx, engine, url, selected, outputDir, tmpDir, saveName,
			headerMap, concurrency, maxSpeed, mode, ffmpegPath, autoSub, subOnly)
	}
}

// ── Interactive Mode ──────────────────────────────────────────────────

func hasStdinPiped() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// interactiveMode provides a one-shot input experience.
// The user enters everything in a single line, or presses Enter for a guided one-line prompt.
func interactiveMode() (url, outputDir, tmpDir, saveName string, concurrency int, maxSpeed int64,
	mergeMode string, ffmpegDir string, headers stringSlice, keys stringSlice, autoSub, subOnly bool, svSelect string) {

	reader := bufio.NewReader(os.Stdin)
	mergeMode = "ts2mp4"

	fmt.Printf("\n")
	fmt.Printf("  %s%sGOm3u8DL%s — Stream Downloader\n", cyan, bold, reset)
	fmt.Printf("  %sPure Go HLS / DASH / MSS%s\n\n", dim, reset)

	// Show usage hint
	fmt.Printf("  %sUsage:%s <URL> [flags]    %s(flags are optional, press Enter for defaults)%s\n\n", bold, reset, dim, reset)
	fmt.Printf("  %sAvailable flags:%s\n", dim, reset)
	fmt.Printf("    -save-dir <dir>       Output directory     %s(default: ./downloads)%s\n", grey, reset)
	fmt.Printf("    -tmp-dir <dir>        Temp directory       %s(default: {save-dir}/)%s\n", grey, reset)
	fmt.Printf("    -save-name <name>     Output filename      %s(default: auto)%s\n", grey, reset)
	fmt.Printf("    -thread-num <n>       Thread count         %s(default: 8)%s\n", grey, reset)
	fmt.Printf("    -max-speed <n>        Speed limit          %s(e.g. 2M, 500K, default: unlimited)%s\n", grey, reset)
	fmt.Printf("    -merge <mode>         Merge mode           %s(binary/ts2mp4/fmp4/ffmpeg/no, default: ts2mp4)%s\n", grey, reset)
	fmt.Printf("    -ffmpeg-dir <path>    ffmpeg path          %s(binary or directory)%s\n", grey, reset)
	fmt.Printf("    -H <header>           HTTP header          %s(repeatable, Key: Value)%s\n", grey, reset)
	fmt.Printf("    -key <kid:key>        Decryption key       %s(repeatable, hex)%s\n", grey, reset)
	fmt.Printf("    -sv <filter>          Stream filter        %s(e.g. res=1920x1080)%s\n", grey, reset)
	fmt.Printf("    -auto-subtitle-fix    Auto-fix subtitle timing\n")
	fmt.Printf("    -sub-only             Download subtitles only\n\n")

	// Single line input
	for {
		fmt.Printf("  %s▶%s ", green, reset)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Printf("    %s⚠ URL is required, please enter at least a URL%s\n\n", red, reset)
			continue
		}

		args := splitCommandLine(line)

		// First non-flag arg is the URL
		var remaining []string
		for i := 0; i < len(args); i++ {
			if args[i][0] == '-' {
				// It's a flag, collect it and its value (if any)
				remaining = append(remaining, args[i])
				// Boolean flags don't have values
				if args[i] == "-auto-subtitle-fix" || args[i] == "-sub-only" || args[i] == "-version" {
					continue
				}
				// Next arg might be the value
				if i+1 < len(args) && args[i+1][0] != '-' {
					i++
					remaining = append(remaining, args[i])
				}
			} else {
				// First non-flag is URL
				if url == "" {
					url = args[i]
				} else {
					remaining = append(remaining, args[i])
				}
			}
		}

		if url == "" {
			fmt.Printf("    %s⚠ No URL found in input%s\n\n", red, reset)
			continue
		}

		// Parse the remaining flags
		fs := flag.NewFlagSet("interactive", flag.ContinueOnError)
		fs.StringVar(&outputDir, "save-dir", "./downloads", "")
		fs.StringVar(&tmpDir, "tmp-dir", "", "")
		fs.StringVar(&saveName, "save-name", "", "")
		fs.IntVar(&concurrency, "thread-num", 8, "")
		fs.Int64Var(&maxSpeed, "max-speed", 0, "")
		fs.StringVar(&mergeMode, "merge", "ts2mp4", "")
		fs.StringVar(&ffmpegDir, "ffmpeg-dir", "", "")
		fs.Var(&headers, "H", "")
		fs.Var(&keys, "key", "")
		fs.BoolVar(&autoSub, "auto-subtitle-fix", false, "")
		fs.BoolVar(&subOnly, "sub-only", false, "")
		fs.StringVar(&svSelect, "sv", "", "")

		if err := fs.Parse(remaining); err != nil {
			fmt.Printf("    %s⚠ Parse error: %v%s\n\n", red, reset, err)
			url = ""
			continue
		}
		break
	}

	fmt.Println()
	return
}

// splitCommandLine splits a line into arguments, respecting double-quoted strings.
func splitCommandLine(line string) []string {
	var args []string
	var current strings.Builder
	inQuote := false

	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func parseSpeed(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0
	}
	mul := int64(1)
	if strings.HasSuffix(s, "G") {
		mul = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "M") {
		mul = 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "K") {
		mul = 1024
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mul))
}

// ── Banner ────────────────────────────────────────────────────────────

func printBanner(url string) {
	fmt.Printf("\n")
	fmt.Printf("  %s%s+-----------------------------------------------------------+%s\n", cyan, bold, reset)
	fmt.Printf("  %s%s| N_m3u8DL-RE (Go Version)  by lullabyable                  |%s\n", cyan, bold, reset)
	fmt.Printf("  %s%s+-----------------------------------------------------------+%s\n", cyan, bold, reset)
	fmt.Printf("  %s│%s %s\n", cyan, reset, url)
	fmt.Printf("  %s%s+-----------------------------------------------------------+%s\n\n", cyan, bold, reset)
}

// ── Stream display (matches original N_m3u8DL-RE format) ─────────────

// streamToString prints full info like the original: "Vid 1920x1080 | 5000 Kbps | ..."
func streamToString(s model.StreamInfo) string {
	var prefix, detail string
	switch s.MediaType {
	case model.MediaTypeAudio:
		prefix = fmt.Sprintf("%sAud%s", blue, reset)
		detail = fmt.Sprintf("%s | %s | %s | %s | %s | %s",
			s.GroupID, formatBW(s.Bandwidth), s.Name, s.Codecs, s.Language, s.Channels)
	case model.MediaTypeSubtitles:
		prefix = fmt.Sprintf("%sSub%s", blue, reset)
		detail = fmt.Sprintf("%s | %s | %s | %s | %s",
			s.GroupID, s.Language, s.Name, s.Codecs, s.Role)
	default:
		prefix = fmt.Sprintf("%sVid%s", cyan, reset)
		detail = fmt.Sprintf("%s | %s | %s | %.0f | %s | %s",
			s.Resolution, formatBW(s.Bandwidth), s.GroupID, s.FrameRate, s.Codecs, s.VideoRange)
	}
	if s.SegmentsCount > 0 {
		segStr := "Segments"
		if s.SegmentsCount == 1 {
			segStr = "Segment"
		}
		detail += fmt.Sprintf(" | %d %s", s.SegmentsCount, segStr)
	}
	// Clean up empty fields
	detail = cleanPipeString(detail)
	return fmt.Sprintf("%s %s", prefix, detail)
}

// streamToShortString is the compact format used for the progress task description.
func streamToShortString(s model.StreamInfo) string {
	switch s.MediaType {
	case model.MediaTypeAudio:
		return fmt.Sprintf("%sAud%s %s | %s | %s | %s",
			blue, reset, formatBW(s.Bandwidth), s.Name, s.Language, s.Role)
	case model.MediaTypeSubtitles:
		return fmt.Sprintf("%sSub%s %s | %s | %s",
			blue, reset, s.Language, s.Name, s.Role)
	default:
		return fmt.Sprintf("%sVid%s %s | %s | %.0f | %s | %s",
			cyan, reset, s.Resolution, formatBW(s.Bandwidth), s.FrameRate, s.Codecs, s.VideoRange)
	}
}

func cleanPipeString(s string) string {
	s = strings.Trim(s, " |")
	for strings.Contains(s, "|  |") {
		s = strings.ReplaceAll(s, "|  |", "|")
	}
	return s
}

func formatBW(bw int) string {
	if bw >= 1_000_000 {
		return fmt.Sprintf("%.1f Mbps", float64(bw)/1_000_000)
	}
	return fmt.Sprintf("%d Kbps", bw/1000)
}

// ── Interactive Stream Selection ──────────────────────────────────────

type indexedStream struct {
	display string
	stream  *model.StreamInfo
}

func interactiveStreamSelect(streams []model.StreamInfo) *model.StreamInfo {
	if len(streams) == 0 {
		return nil
	}
	if len(streams) == 1 {
		return &streams[0]
	}

	// Group by type
	var vids, auds, subs []model.StreamInfo
	for _, s := range streams {
		switch s.MediaType {
		case model.MediaTypeAudio:
			auds = append(auds, s)
		case model.MediaTypeSubtitles:
			subs = append(subs, s)
		default:
			vids = append(vids, s)
		}
	}

	fmt.Printf("\n%s[info]%s Please select streams:\n\n", cyan, reset)

	idx := 0
	typeOrder := []struct {
		name    string
		color   string
		streams []model.StreamInfo
	}{
		{"Video", cyan, vids},
		{"Audio", blue, auds},
		{"Subtitle", magenta, subs},
	}

	var all []indexedStream

	for _, g := range typeOrder {
		if len(g.streams) == 0 {
			continue
		}
		fmt.Printf("  %s%s── %s ──%s\n", bold, g.color, g.name, reset)
		for _, s := range g.streams {
			idx++
			marker := "  "
			// Auto-select first video + first audio per language
			if idx == 1 || (g.name == "Audio" && !alreadySelectedLang(all, s.Language)) {
				marker = fmt.Sprintf("%s▶%s", green, reset)
			} else {
				marker = fmt.Sprintf("%s ○%s", dim, reset)
			}
			display := fmt.Sprintf("  %s %3d. %s", marker, idx, streamToString(s))
			fmt.Println(display)
			all = append(all, indexedStream{display: display, stream: &s})
		}
		fmt.Println()
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("  %s▶%s Select stream number %s[1-%d]%s: ", green, reset, dim, len(streams), reset)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return all[0].stream // default to first
		}
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(streams) {
			fmt.Printf("    %s⚠ Invalid choice%s\n", red, reset)
			continue
		}
		return all[n-1].stream
	}
}

func alreadySelectedLang(all []indexedStream, lang string) bool {
	for _, a := range all {
		if a.stream.MediaType == model.MediaTypeAudio && a.stream.Language == lang {
			return true
		}
	}
	return false
}

// ── Progress Display (N_m3u8DL-RE style) ─────────────────────────────

// renderProgress renders the live download status in N_m3u8DL-RE style:
//
//	task description  ██████████████████░░░░░░░░░  128/187  68.45%  156.22MB/228.55MB  12.45MBps  00m05s  ⠹
func renderProgress(desc string, e m3u8dl.ProgressEvent) {
	// Progress bar
	barW := 25
	filled := int(e.Percent / 100 * float64(barW))
	if filled > barW {
		filled = barW
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)

	// Stats
	doneTotal := fmt.Sprintf("%d/%d", e.SegmentsDone, e.Segments)
	pctStr := fmt.Sprintf("%6.2f%%", e.Percent)
	sizeStr := fmt.Sprintf("%s/%s", formatFileSize(float64(e.Downloaded)), formatFileSize(float64(e.Total)))
	speedStr := formatFileSize(float64(e.Speed)) + "ps"
	etaStr := formatTime(int(e.ETA))
	spin := nextSpinner()

	// Compose the line — match original N_m3u8DL-RE column layout:
	// [description] [bar] done/total percent  size  speed  eta  spinner
	line := fmt.Sprintf("  %-40s %s  %s %s  %-18s  %s%-11s%s  %s%-8s%s  %s",
		truncateDesc(desc, 40),
		bar,
		doneTotal,
		pctStr,
		sizeStr,
		green, speedStr, reset,
		yellow, etaStr, reset,
		spin)

	// Use \r to overwrite the same line (no trailing newline)
	fmt.Fprintf(os.Stderr, "\r%s%s", eraseLine, line)
}

// renderProgressDone shows the final completed state (bar fully filled, green).
func renderProgressDone(desc string, e m3u8dl.ProgressEvent) {
	barW := 25
	bar := strings.Repeat("█", barW)
	doneTotal := fmt.Sprintf("%d/%d", e.Segments, e.Segments)
	pctStr := fmt.Sprintf("%6.2f%%", 100.0)
	sizeStr := formatFileSize(float64(e.Total))

	line := fmt.Sprintf("  %-40s %s%s%s  %s %s  %-18s  %s  %s",
		truncateDesc(desc, 40),
		green, bar, reset,
		doneTotal,
		pctStr,
		sizeStr,
		"done!",
		"✓")

	// Overwrite current line then print newline to commit
	fmt.Fprintf(os.Stderr, "\r%s%s\n", eraseLine, line)
}

// clearProgress erases the current progress line.
func clearProgress() {
	fmt.Fprintf(os.Stderr, "\r%s", eraseLine)
}



func truncateDesc(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// ── Formatting helpers (matching N_m3u8DL-RE style) ───────────────────

// formatFileSize matches the original C# FormatFileSize: 2 decimal places, no space.
func formatFileSize(bytes float64) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.2fGB", bytes/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.2fMB", bytes/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.2fKB", bytes/1024)
	default:
		return fmt.Sprintf("%.2fB", bytes)
	}
}

// formatTime matches the original C# FormatTime: HHh MMm SSs.
func formatTime(seconds int) string {
	if seconds <= 0 || seconds > 36000 {
		return "--m--s"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%02dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%02dm%02ds", m, s)
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

// ── Download Functions ────────────────────────────────────────────────

func downloadSeparateStreams(ctx context.Context, engine *m3u8dl.Engine, url string,
	videoStreams, audioStreams []model.StreamInfo, svSelect, outputDir, tmpDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	ffmpegPath string, autoSub, subOnly bool) {

	// Select video
	var selectedVideo *model.StreamInfo
	if svSelect != "" {
		selectedVideo = selectStreamByFilter(videoStreams, svSelect)
	} else if len(videoStreams) == 1 {
		selectedVideo = &videoStreams[0]
	} else {
		selectedVideo = interactiveStreamSelect(videoStreams)
	}

	// Select audio
	var selectedAudio *model.StreamInfo
	if len(audioStreams) == 1 {
		selectedAudio = &audioStreams[0]
	} else {
		selectedAudio = interactiveStreamSelect(audioStreams)
	}

	if selectedVideo == nil || selectedAudio == nil {
		fmt.Fprintf(os.Stderr, "%s[error]%s Failed to select streams\n", red, reset)
		os.Exit(1)
	}

	fmt.Printf("\n%s[info]%s Downloading:\n", cyan, reset)
	fmt.Printf("  %sVideo:%s %s\n", bold, reset, streamToShortString(*selectedVideo))
	fmt.Printf("  %sAudio:%s %s\n", bold, reset, streamToShortString(*selectedAudio))

	videoDesc := streamToShortString(*selectedVideo)
	audioDesc := streamToShortString(*selectedAudio)

	// Create root temp directory: {save-dir}/{saveName}_tmp/
	// video_tmp/ and audio_tmp/ will be created inside by the engine.
	baseDir := tmpDir
	if baseDir == "" {
		baseDir = outputDir
	}
	rootTmp := filepath.Join(baseDir, saveName+"_tmp")
	if err := os.MkdirAll(rootTmp, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "%s[error]%s Create temp dir: %v\n", red, reset, err)
		os.Exit(1)
	}
	fmt.Printf("%s[info]%s Temp dir: %s\n", cyan, reset, rootTmp)

	var lastProgressTime time.Time
	handler := m3u8dl.EventHandlerFunc{
		OnProgressFn: func(e m3u8dl.ProgressEvent) {
			now := time.Now()
			if now.Sub(lastProgressTime) < 200*time.Millisecond {
				return
			}
			lastProgressTime = now
			renderProgress(videoDesc, e)
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			clearProgress()
			fmt.Printf("  %s[%s]%s %s %s\n", dim, e.Status, reset, e.TaskID, reset)
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				clearProgress()
				fmt.Printf("  %s[warn]%s %s\n", yellow, reset, e.Message)
			}
		},
	}

	// Download video
	fmt.Printf("\n%s[info]%s Downloading video segments...\n", cyan, reset)
	fmt.Print(hideCur) // hide cursor during progress
	videoReq := model.DownloadRequest{
		Stream:             selectedVideo,
		URL:                url,
		OutputDir:          outputDir,
		TmpDir:             filepath.Join(rootTmp, "video_tmp"),
		SaveName:           saveName,
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		DelAfterDone:       false,
	}
	videoResult, err := engine.DownloadOnly(ctx, videoReq, handler)
	fmt.Print(showCur)
	if err != nil {
		clearProgress()
		fmt.Fprintf(os.Stderr, "\n%s[error]%s Video download failed: %v\n", red, reset, err)
		os.Exit(1)
	}
	clearProgress()
	fmt.Printf("%s[info]%s Video done: %d segments\n", green, reset, len(videoResult.SegmentPaths))

	// Download audio
	fmt.Printf("\n%s[info]%s Downloading audio segments...\n", cyan, reset)
	fmt.Print(hideCur)
	handler.OnProgressFn = func(e m3u8dl.ProgressEvent) {
		now := time.Now()
		if now.Sub(lastProgressTime) < 200*time.Millisecond {
			return
		}
		lastProgressTime = now
		renderProgress(audioDesc, e)
	}
	audioReq := model.DownloadRequest{
		Stream:             selectedAudio,
		URL:                url,
		OutputDir:          outputDir,
		TmpDir:             filepath.Join(rootTmp, "audio_tmp"),
		SaveName:           saveName,
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		DelAfterDone:       false,
	}
	audioResult, err := engine.DownloadOnly(ctx, audioReq, handler)
	fmt.Print(showCur)
	if err != nil {
		clearProgress()
		fmt.Fprintf(os.Stderr, "\n%s[error]%s Audio download failed: %v\n", red, reset, err)
		os.Exit(1)
	}
	clearProgress()
	fmt.Printf("%s[info]%s Audio done: %d segments\n", green, reset, len(audioResult.SegmentPaths))

	// Mux (skip if MergeModeNo)
	outputPath := filepath.Join(outputDir, saveName+".mp4")

	if mode == model.MergeModeNo {
		fmt.Printf("\n%s[info]%s Download only mode — segments saved to: %s\n", cyan, reset, rootTmp)
		fmt.Printf("%s[info]%s Video: %d segments in %s\n", green, reset, len(videoResult.SegmentPaths), filepath.Join(rootTmp, "video_tmp"))
		fmt.Printf("%s[info]%s Audio: %d segments in %s\n", green, reset, len(audioResult.SegmentPaths), filepath.Join(rootTmp, "audio_tmp"))
		printDone(rootTmp)
		return
	}

	fmt.Printf("\n%s[info]%s Muxing %d video + %d audio → %s (%s)\n",
		cyan, reset, len(videoResult.SegmentPaths), len(audioResult.SegmentPaths), outputPath, mergeModeStr(mode))

	isTS := isTSFormat(videoResult.SegmentPaths)
	var muxErr error
	switch mode {
	case model.MergeModeTS2MP4:
		if isTS {
			muxErr = merge.MuxSeparateTSStreams(videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			muxErr = merge.MuxFMP4FromSegments(videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}
	case model.MergeModeFMP4:
		muxErr = merge.MuxFMP4FromSegments(videoResult.InitPath, audioResult.InitPath,
			videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
	case model.MergeModeFFmpeg:
		vm := filepath.Join(rootTmp, "video_merged.ts")
		am := filepath.Join(rootTmp, "audio_merged.ts")
		merge.BinaryMerge(videoResult.SegmentPaths, vm)
		merge.BinaryMerge(audioResult.SegmentPaths, am)
		muxErr = merge.FFmpegMuxAV(vm, am, outputPath, ffmpegPath)
	default:
		if isTS {
			muxErr = merge.MuxSeparateTSStreams(videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		} else {
			muxErr = merge.MuxFMP4FromSegments(videoResult.InitPath, audioResult.InitPath,
				videoResult.SegmentPaths, audioResult.SegmentPaths, outputPath)
		}
	}

	// Clean up entire rootTmp (video_tmp + audio_tmp + any temp files)
	os.RemoveAll(rootTmp)

	if muxErr != nil {
		fmt.Fprintf(os.Stderr, "%s[error]%s Mux failed: %v\n", red, reset, muxErr)
		os.Exit(1)
	}

	printDone(outputPath)
}

func downloadSingleStream(ctx context.Context, engine *m3u8dl.Engine, url string,
	selected *model.StreamInfo, outputDir, tmpDir, saveName string,
	headerMap map[string]string, concurrency int, maxSpeed int64, mode model.MergeMode,
	ffmpegPath string, autoSub, subOnly bool) {

	startTime := time.Now()

	desc := streamToShortString(*selected)
	var lastProgressTime time.Time

	req := model.DownloadRequest{
		Stream:             selected,
		URL:                url,
		OutputDir:          outputDir,
		TmpDir:             tmpDir,
		SaveName:           saveName,
		Headers:            headerMap,
		ThreadCount:        concurrency,
		MaxSpeed:           maxSpeed,
		DownloadRetryCount: 3,
		MergeMode:          mode,
		FFmpegPath:         ffmpegPath,
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
			renderProgress(desc, e)
		},
		OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
			clearProgress()
			statusLabel := strings.ToUpper(e.Status.String())
			switch e.Status {
			case model.TaskStatusDone:
				fmt.Printf("  %s[done]%s %s\n", green, reset, e.TaskID)
			case model.TaskStatusFailed:
				fmt.Printf("  %s[fail]%s %s\n", red, reset, e.TaskID)
			case model.TaskStatusMerging:
				fmt.Printf("  %s[merge]%s %s\n", blue, reset, e.TaskID)
			default:
				fmt.Printf("  %s[%s]%s %s\n", dim, statusLabel, reset, e.TaskID)
			}
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				clearProgress()
				fmt.Printf("  %s[warn]%s %s\n", yellow, reset, e.Message)
			}
		},
	}

	fmt.Printf("\n%s[info]%s Starting download...\n", cyan, reset)
	fmt.Print(hideCur) // hide cursor during progress

	if err := engine.Download(ctx, req, handler); err != nil {
		fmt.Print(showCur)
		clearProgress()
		fmt.Fprintf(os.Stderr, "\n%s[error]%s Download failed: %v\n", red, reset, err)
		os.Exit(1)
	}

	fmt.Print(showCur) // restore cursor
	elapsed := time.Since(startTime).Seconds()

	outputPath := buildOutputPath(outputDir, saveName, mode)
	var fileSize int64
	if fi, err := os.Stat(outputPath); err == nil {
		fileSize = fi.Size()
	}

	// Final progress line with full bar
	renderProgressDone(desc, m3u8dl.ProgressEvent{
		Segments:     selected.SegmentsCount,
		SegmentsDone: selected.SegmentsCount,
		Total:        fileSize,
		Downloaded:   fileSize,
	})

	fmt.Printf("\n")
	fmt.Printf("%s%s  Done!  %s  Output: %s  Size: %s  Time: %s\n",
		bgGreen, white+bold, reset, outputPath, formatFileSize(float64(fileSize)), formatDuration(elapsed))
	fmt.Printf("\n")
}

func printDone(path string) {
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	fmt.Printf("\n%s%s  Done!  %s  Output: %s  Size: %s\n\n",
		bgGreen, white+bold, reset, path, formatFileSize(float64(size)))
}

// ── Utilities ─────────────────────────────────────────────────────────

func generateSaveName(url string) string {
	return time.Now().Format("20060102") + "+" + strconv.FormatInt(time.Now().Unix(), 10)
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
	case "no":
		return model.MergeModeNo
	default:
		return model.MergeModeTS2MP4
	}
}

func buildOutputPath(dir, name string, mode model.MergeMode) string {
	if dir == "" {
		dir = "."
	}
	if mode == model.MergeModeBinary {
		return filepath.Join(dir, name+".ts")
	}
	if mode == model.MergeModeNo {
		return filepath.Join(dir, name+"_tmp")
	}
	return filepath.Join(dir, name+".mp4")
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
	n, _ := f.Read(buf)
	for i := 0; i < n; i++ {
		if buf[i] == 0x47 {
			return true
		}
	}
	return false
}

// ── Stream Filter (sv) ────────────────────────────────────────────────

type svFilter struct {
	idRegex     *regexp.Regexp
	langRegex   *regexp.Regexp
	nameRegex   *regexp.Regexp
	codecsRegex *regexp.Regexp
	resRegex    *regexp.Regexp
	frameRegex  *regexp.Regexp
	segsMin     int
	segsMax     int
	chRegex     *regexp.Regexp
	rangeRegex  *regexp.Regexp
	urlRegex    *regexp.Regexp
	bwMin       int
	bwMax       int
	role        string
	forMode     string
}

func parseSVFilter(raw string) (*svFilter, error) {
	f := &svFilter{forMode: "best"}
	for _, part := range strings.Split(raw, ":") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid -sv token: %q", part)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		var rx *regexp.Regexp
		var err error
		switch strings.ToLower(key) {
		case "id":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.idRegex = rx
		case "lang", "language":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.langRegex = rx
		case "name":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.nameRegex = rx
		case "codecs":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.codecsRegex = rx
		case "res", "resolution":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.resRegex = rx
		case "frame", "framerate":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.frameRegex = rx
		case "segsmin":
			f.segsMin, _ = strconv.Atoi(val)
		case "segsmax":
			f.segsMax, _ = strconv.Atoi(val)
		case "ch", "channels":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.chRegex = rx
		case "range":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.rangeRegex = rx
		case "url":
			rx, err = regexp.Compile(val)
			if err != nil {
				return nil, err
			}
			f.urlRegex = rx
		case "bwmin":
			f.bwMin, _ = strconv.Atoi(val)
		case "bwmax":
			f.bwMax, _ = strconv.Atoi(val)
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
	if f.frameRegex != nil && !f.frameRegex.MatchString(fmt.Sprintf("%.2f", s.FrameRate)) {
		return false
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

func selectStreamByFilter(streams []model.StreamInfo, svRaw string) *model.StreamInfo {
	f, err := parseSVFilter(svRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s[error]%s %v\n", red, reset, err)
		os.Exit(1)
	}
	type scored struct {
		stream *model.StreamInfo
	}
	var matches []scored
	for i := range streams {
		if streamMatches(&streams[i], f) {
			matches = append(matches, scored{stream: &streams[i]})
		}
	}
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "%s[error]%s No streams match -sv filter\n", red, reset)
		os.Exit(1)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].stream.Bandwidth > matches[j].stream.Bandwidth
	})

	switch {
	case f.forMode == "all":
		var all []model.StreamInfo
		for _, m := range matches {
			all = append(all, *m.stream)
		}
		return interactiveStreamSelect(all)
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

// ── Misc ──────────────────────────────────────────────────────────────

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// findFFmpeg locates the ffmpeg binary. Search order:
// 1. User-specified path (from -ffmpeg-dir or config)
// 2. System PATH (just run "ffmpeg")
// If not found, prompts the user to enter the path.
func findFFmpeg(userPath string) string {
	// 1. User-specified path
	if userPath != "" {
		if _, err := os.Stat(userPath); err == nil {
			return userPath
		}
		// Maybe it's a directory
		candidate := filepath.Join(userPath, "ffmpeg")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		candidate = filepath.Join(userPath, "ffmpeg.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		fmt.Printf("  %s[warn]%s ffmpeg not found at: %s\n", yellow, reset, userPath)
	}

	// 2. System PATH
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return "ffmpeg"
	}

	// 3. Prompt user
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("  %s[warn]%s ffmpeg not found in PATH\n", yellow, reset)
		fmt.Printf("  %s▶%s Enter ffmpeg path (or install it and press Enter to retry): ", green, reset)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			// Retry PATH check
			if _, err := exec.LookPath("ffmpeg"); err == nil {
				return "ffmpeg"
			}
			continue
		}
		if _, err := os.Stat(line); err == nil {
			return line
		}
		fmt.Printf("  %s[error]%s File not found: %s\n", red, reset, line)
	}
}
