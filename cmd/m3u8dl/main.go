package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lullabyable/GOm3u8DL/pkg/downloader"
	"github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	var (
		url          string
		outputDir    string
		saveName     string
		concurrency  int
		maxSpeed     int64
		mergeMode    string
		headers      stringSlice
		keys         stringSlice
		autoSub      bool
		subOnly      bool
		showVersion  bool
	)

	flag.StringVar(&url, "url", "", "M3U8/MPD/ISM URL (required)")
	flag.StringVar(&outputDir, "o", ".", "Output directory")
	flag.StringVar(&saveName, "save-name", "", "Output filename (without extension)")
	flag.IntVar(&concurrency, "concurrency", 8, "Segment download concurrency")
	flag.Int64Var(&maxSpeed, "max-speed", 0, "Max download speed in bytes/sec (0=unlimited)")
	flag.StringVar(&mergeMode, "merge", "binary", "Merge mode: binary, ts2mp4, fmp4, ffmpeg")
	flag.Var(&headers, "H", "HTTP header (repeatable, format: Key: Value)")
	flag.Var(&keys, "key", "Decryption key in kid:key hex format (repeatable)")
	flag.BoolVar(&autoSub, "auto-subtitle-fix", false, "Auto-fix subtitle timing")
	flag.BoolVar(&subOnly, "sub-only", false, "Download subtitles only")
	flag.BoolVar(&showVersion, "version", false, "Show version")
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

	// Progress handler
	progressFn := func(p downloader.Progress) {
		fmt.Fprintf(os.Stderr, "\r  %.1f%% | %d/%d segments | %s/s | ETA %.0fs   ",
			p.Percent, p.SegmentsDone, p.Segments,
			formatBytes(p.Speed), p.ETA)
	}

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

	// Select stream (pick highest bandwidth)
	var selected *model.StreamInfo
	for i := range streams {
		s := &streams[i]
		if selected == nil || s.Bandwidth > selected.Bandwidth {
			selected = s
		}
	}

	fmt.Printf("Selected: %s (%s)\n", selected.Resolution, selected.FormatBandwidth())

	// Download
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
	}

	handler := m3u8dl.EventHandlerFunc{
		OnProgressFn: func(e m3u8dl.ProgressEvent) {
			progressFn(downloader.Progress{
				Percent:      e.Percent,
				SegmentsDone: e.SegmentsDone,
				Segments:     e.Segments,
				Speed:        e.Speed,
				ETA:          e.ETA,
			})
		},
		OnLogFn: func(e m3u8dl.LogEvent) {
			if e.Level >= m3u8dl.LogWarn {
				fmt.Fprintf(os.Stderr, "\n[%s] %s", logLevelStr(e.Level), e.Message)
			}
		},
	}

	if err := engine.Download(ctx, req, handler); err != nil {
		fmt.Fprintf(os.Stderr, "\nDownload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nDone!")

	// Cleanup if requested
	if req.DelAfterDone {
		_ = downloader.CleanupTemp(outputDir + "/temp")
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
