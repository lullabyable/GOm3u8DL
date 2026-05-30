package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// ProgressFunc is called periodically with download progress.
type ProgressFunc func(Progress)

// Manager orchestrates downloading all segments of a playlist.
type Manager struct {
	client    *http.Client
	concur    int
	retries   int
	onProgress ProgressFunc
}

// NewManager creates a new download manager.
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		client:  &http.Client{Timeout: 30 * time.Second},
		concur:  8,
		retries: 3,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ManagerOption configures the Manager.
type ManagerOption func(*Manager)

func WithConcurrency(n int) ManagerOption {
	return func(m *Manager) { m.concur = n }
}

func WithRetries(n int) ManagerOption {
	return func(m *Manager) { m.retries = n }
}

func WithHTTPClient(c *http.Client) ManagerOption {
	return func(m *Manager) { m.client = c }
}

func WithProgressFunc(fn ProgressFunc) ManagerOption {
	return func(m *Manager) { m.onProgress = fn }
}

// DownloadSegments downloads all segments from a playlist and returns the file paths in order.
func (m *Manager) DownloadSegments(ctx context.Context, playlist *model.Playlist, tempDir string) ([]string, error) {
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", tempDir, err)
	}

	// Collect all segments from all parts
	var allSegments []model.MediaSegment
	for _, part := range playlist.MediaParts {
		allSegments = append(allSegments, part.MediaSegments...)
	}

	if len(allSegments) == 0 {
		return nil, fmt.Errorf("no segments to download")
	}

	// Download init segment if present
	if playlist.MediaInit != nil {
		sd := NewSegmentDownloader(m.client, m.retries)
		result := sd.Download(ctx, *playlist.MediaInit, tempDir)
		if result.Error != nil {
			return nil, fmt.Errorf("download init segment: %w", result.Error)
		}
	}

	tracker := NewProgressTracker(len(allSegments))

	// Progress reporting goroutine
	if m.onProgress != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					m.onProgress(tracker.Progress())
				}
			}
		}()
	}

	// Worker pool
	type indexedResult struct {
		index  int
		result DownloadResult
	}

	jobs := make(chan int, len(allSegments))
	results := make(chan indexedResult, len(allSegments))

	var wg sync.WaitGroup
	for i := 0; i < m.concur; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sd := NewSegmentDownloader(m.client, m.retries)
			for idx := range jobs {
				seg := allSegments[idx]
				result := sd.Download(ctx, seg, tempDir)
				if result.Error == nil {
					tracker.AddBytes(result.Bytes)
					tracker.SegmentDone()
				}
				results <- indexedResult{index: idx, result: result}
			}
		}()
	}

	// Enqueue jobs
	for i := range allSegments {
		jobs <- i
	}
	close(jobs)

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	paths := make([]string, len(allSegments))
	var firstErr error
	for r := range results {
		if r.result.Error != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("segment %d: %w", r.index, r.result.Error)
			}
			continue
		}
		paths[r.index] = r.result.FilePath
	}

	if firstErr != nil {
		return nil, firstErr
	}

	// Final progress report
	if m.onProgress != nil {
		m.onProgress(tracker.Progress())
	}

	return paths, nil
}

// CleanupTemp removes the temp directory and all downloaded segments.
func CleanupTemp(tempDir string) error {
	return os.RemoveAll(tempDir)
}

// SegmentPath returns the expected path for a downloaded segment.
func SegmentPath(tempDir string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("seg_%d.ts", index))
}
