package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
	"github.com/lullabyable/GOm3u8DL/pkg/parser/hls"
)

// LiveRecorder records HLS live streams by periodically polling
// the playlist and appending new segments as they appear.
type LiveRecorder struct {
	client      *http.Client
	concurrency int
	retries     int
	onProgress  ProgressFunc
	onSegment   func(seg model.MediaSegment)

	mu          sync.Mutex
	downloaded  map[string]bool // tracks segment URLs already downloaded
	segIndex    int             // global segment counter for file naming
	outputPath  string
	outputFile  *os.File
	totalBytes  int64
	totalSegs   int
	discontCount int
}

// LiveRecorderOption configures a LiveRecorder.
type LiveRecorderOption func(*LiveRecorder)

func WithLiveConcurrency(n int) LiveRecorderOption {
	return func(r *LiveRecorder) { r.concurrency = n }
}

func WithLiveRetries(n int) LiveRecorderOption {
	return func(r *LiveRecorder) { r.retries = n }
}

func WithLiveHTTPClient(c *http.Client) LiveRecorderOption {
	return func(r *LiveRecorder) { r.client = c }
}

func WithLiveProgress(fn ProgressFunc) LiveRecorderOption {
	return func(r *LiveRecorder) { r.onProgress = fn }
}

func WithLiveOnSegment(fn func(model.MediaSegment)) LiveRecorderOption {
	return func(r *LiveRecorder) { r.onSegment = fn }
}

// NewLiveRecorder creates a new LiveRecorder with the given options.
func NewLiveRecorder(opts ...LiveRecorderOption) *LiveRecorder {
	r := &LiveRecorder{
		client:      &http.Client{Timeout: 30 * time.Second},
		concurrency: 4,
		retries:     3,
		downloaded:  make(map[string]bool),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Record starts recording a live HLS stream. It periodically fetches the
// playlist, downloads new segments, and appends them to a single output file
// in outputDir. It blocks until the stream ends (#EXT-X-ENDLIST), the context
// is cancelled, or a fatal error occurs.
func (r *LiveRecorder) Record(ctx context.Context, playlistURL string, outputDir string, headers map[string]string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outputDir, err)
	}

	r.outputPath = filepath.Join(outputDir, "live_output.ts")
	f, err := os.Create(r.outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	r.outputFile = f
	defer f.Close()

extractor := hls.NewExtractor(playlistURL)

	// Initial poll interval; will be updated from playlist target duration.
	pollInterval := 3 * time.Second

	// Progress reporter
	if r.onProgress != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					r.onProgress(r.snapshotProgress())
				}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		body, err := r.fetchPlaylist(ctx, playlistURL, headers)
		if err != nil {
			// Network error — retry after a short delay
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
				continue
			}
		}

		_, playlist, err := extractor.Parse(body)
		if err != nil {
			// Parse error — retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
				continue
			}
		}

		// Update poll interval from target duration
		if playlist.TargetDuration != nil {
			pollInterval = time.Duration(*playlist.TargetDuration * float64(time.Second))
			// Clamp to reasonable bounds
			if pollInterval < 1*time.Second {
				pollInterval = 1 * time.Second
			}
			if pollInterval > 30*time.Second {
				pollInterval = 30 * time.Second
			}
		}

		// Download any new segments
		for _, part := range playlist.MediaParts {
			for _, seg := range part.MediaSegments {
				if err := r.processSegment(ctx, seg, headers); err != nil {
					// Non-fatal: log and continue
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
						continue
					}
				}
			}
		}

		// Check for live end
		if !playlist.IsLive {
			return nil
		}

		// Wait before next poll
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// OutputPath returns the path of the recorded output file.
func (r *LiveRecorder) OutputPath() string {
	return r.outputPath
}

// BytesWritten returns total bytes written to the output.
func (r *LiveRecorder) BytesWritten() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalBytes
}

// SegmentsRecorded returns the number of segments recorded.
func (r *LiveRecorder) SegmentsRecorded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalSegs
}

// fetchPlaylist fetches the playlist body with retry.
func (r *LiveRecorder) fetchPlaylist(ctx context.Context, url string, headers map[string]string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= r.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", fmt.Errorf("new request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http status %d", resp.StatusCode)
			continue
		}
		return string(body), nil
	}
	return "", fmt.Errorf("fetch playlist after %d retries: %w", r.retries, lastErr)
}

// processSegment downloads a segment if not already downloaded and appends it.
func (r *LiveRecorder) processSegment(ctx context.Context, seg model.MediaSegment, headers map[string]string) error {
	r.mu.Lock()
	if r.downloaded[seg.URL] {
		r.mu.Unlock()
		return nil
	}
	r.downloaded[seg.URL] = true
	r.mu.Unlock()

	// Notify callback
	if r.onSegment != nil {
		r.onSegment(seg)
	}

	// Download with retry
	data, err := r.downloadSegment(ctx, seg, headers)
	if err != nil {
		return err
	}

	// Append to output file
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.outputFile.Write(data); err != nil {
		return fmt.Errorf("write segment: %w", err)
	}

	r.totalBytes += int64(len(data))
	r.totalSegs++
	r.segIndex++

	return nil
}

// downloadSegment downloads a single segment with retry.
func (r *LiveRecorder) downloadSegment(ctx context.Context, seg model.MediaSegment, headers map[string]string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= r.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, seg.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http status %d", resp.StatusCode)
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("download segment after %d retries: %w", r.retries, lastErr)
}

// snapshotProgress returns a Progress snapshot for the live recorder.
func (r *LiveRecorder) snapshotProgress() Progress {
	r.mu.Lock()
	segs := r.totalSegs
	bytes := r.totalBytes
	r.mu.Unlock()

	return Progress{
		Downloaded:   bytes,
		SegmentsDone: segs,
		Segments:     segs, // unknown total for live
	}
}
