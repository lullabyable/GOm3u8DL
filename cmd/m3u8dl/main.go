package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

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
		autoSelect  bool
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
	flag.BoolVar(&autoSelect, "auto-select", false, "Auto-select best quality stream")
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

	// Interactive stream selection or auto-select
	var selected *model.StreamInfo
	if autoSelect || len(streams) == 1 {
		// Auto-select: pick highest bandwidth video stream
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
	} else {
		selected = selectStreamInteractive(streams)
	}

	if selected == nil {
		fmt.Fprintln(os.Stderr, "No stream selected")
		os.Exit(1)
	}

	fmt.Printf("Selected: %s %s (%s, %d segments)\n",
		selected.Name, selected.Resolution,
		selected.FormatBandwidth(), selected.SegmentsCount)

	// Auto-generate save name if not provided
	if saveName == "" {
		saveName = generateSaveName(url, selected)
	}

	// Progress bar state
	var lastProgressTime time.Time

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

	// Final newline after progress bar
	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
	outputPath := buildOutputPath(outputDir, saveName, mode)
	fmt.Printf("Done! Output: %s\n", outputPath)
}

// selectStreamInteractive displays streams and lets the user choose.
func selectStreamInteractive(streams []model.StreamInfo) *model.StreamInfo {
	// Group by media type
	videoStreams := make([]model.StreamInfo, 0)
	audioStreams := make([]model.StreamInfo, 0)
	subStreams := make([]model.StreamInfo, 0)

	for _, s := range streams {
		switch s.MediaType {
		case model.MediaTypeVideo:
			videoStreams = append(videoStreams, s)
		case model.MediaTypeAudio:
			audioStreams = append(audioStreams, s)
		case model.MediaTypeSubtitles:
			subStreams = append(subStreams, s)
		default:
			videoStreams = append(videoStreams, s)
		}
	}

	// Sort video streams by bandwidth (highest first)
	sort.Slice(videoStreams, func(i, j int) bool {
		return videoStreams[i].Bandwidth > videoStreams[j].Bandwidth
	})

	fmt.Println("\nAvailable streams:")
	fmt.Println(strings.Repeat("─", 72))

	allStreams := make([]model.StreamInfo, 0)
	idx := 1

	if len(videoStreams) > 0 {
		fmt.Println("  Video:")
		for _, s := range videoStreams {
			segInfo := ""
			if s.SegmentsCount > 0 {
				segInfo = fmt.Sprintf(" [%d segs]", s.SegmentsCount)
			}
			fmt.Printf("    [%d] %-12s %-16s %s%s\n",
				idx, s.Name, s.Resolution, s.FormatBandwidth(), segInfo)
			allStreams = append(allStreams, s)
			idx++
		}
	}

	if len(audioStreams) > 0 {
		fmt.Println("  Audio:")
		for _, s := range audioStreams {
			lang := s.Language
			if lang == "" {
				lang = "unknown"
			}
			fmt.Printf("    [%d] %-12s %-16s %s\n", idx, s.Name, lang, s.GroupID)
			allStreams = append(allStreams, s)
			idx++
		}
	}

	if len(subStreams) > 0 {
		fmt.Println("  Subtitles:")
		for _, s := range subStreams {
			lang := s.Language
			if lang == "" {
				lang = "unknown"
			}
			fmt.Printf("    [%d] %-12s %-16s\n", idx, s.Name, lang)
			allStreams = append(subStreams, s)
			idx++
		}
	}

	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("Select stream [1-%d] (default: 1): ", len(allStreams))

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	choice := 1
	if input != "" {
		if n, err := fmt.Sscanf(input, "%d", &choice); n != 1 || err != nil || choice < 1 || choice > len(allStreams) {
			fmt.Fprintf(os.Stderr, "Invalid choice: %s\n", input)
			return nil
		}
	}

	return &allStreams[choice-1]
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

// generateSaveName creates a filename from the URL and stream info.
func generateSaveName(url string, stream *model.StreamInfo) string {
	// Extract last path segment
	parts := strings.Split(url, "/")
	name := "output"
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if idx := strings.Index(last, "?"); idx >= 0 {
			last = last[:idx]
		}
		if idx := strings.Index(last, "."); idx >= 0 {
			last = last[:idx]
		}
		if last != "" {
			name = last
		}
	}
	if stream.Resolution != "" && stream.Resolution != "unknown" {
		name += "_" + strings.ReplaceAll(stream.Resolution, "x", "x")
	}
	return name
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
