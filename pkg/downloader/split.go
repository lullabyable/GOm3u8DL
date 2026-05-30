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
)

// SplitDownloader downloads a single large file by splitting it into byte ranges
// and downloading each part in parallel, then merging them in order.
type SplitDownloader struct {
	client  *http.Client
	retries int
	limiter *Limiter
}

// NewSplitDownloader creates a new split downloader.
func NewSplitDownloader(client *http.Client, retries int, limiter *Limiter) *SplitDownloader {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if retries <= 0 {
		retries = 3
	}
	return &SplitDownloader{
		client:  client,
		retries: retries,
		limiter: limiter,
	}
}

// partInfo describes one byte-range part to download.
type partInfo struct {
	Index int
	Start int64
	End   int64 // inclusive
}

// partTempPath returns the temp file path for a given part.
func partTempPath(tempDir string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("part_%d.tmp", index))
}

// Download downloads a large file by splitting into byte ranges.
// If totalSize <= 0, a HEAD request is made to determine it.
// The file is downloaded to a temp dir, then merged into outputPath.
func (sd *SplitDownloader) Download(ctx context.Context, url string, outputPath string, totalSize int64, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 4
	}

	// HEAD request to get total size if not provided
	if totalSize <= 0 {
		size, err := sd.headSize(ctx, url)
		if err != nil {
			return fmt.Errorf("head request: %w", err)
		}
		totalSize = size
	}

	if totalSize <= 0 {
		return fmt.Errorf("unknown file size (Content-Length missing or 0)")
	}

	// Check if server supports range requests
	supportsRange, err := sd.checkRangeSupport(ctx, url)
	if err != nil {
		return fmt.Errorf("range check: %w", err)
	}

	if !supportsRange || concurrency == 1 {
		// Fallback: single download
		return sd.singleDownload(ctx, url, outputPath)
	}

	// Create temp directory for parts
	tempDir := outputPath + ".parts"
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", tempDir, err)
	}
	defer os.RemoveAll(tempDir)

	// Split into parts
	parts := sd.splitRanges(totalSize, int64(concurrency))

	// Check for existing partial files (resume support)
	completed := make([]bool, len(parts))
	for i, p := range parts {
		partPath := partTempPath(tempDir, p.Index)
		info, err := os.Stat(partPath)
		if err == nil {
			expectedSize := p.End - p.Start + 1
			if info.Size() == expectedSize {
				completed[i] = true
			}
		}
	}

	// Download parts in parallel
	errCh := make(chan error, len(parts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for i, p := range parts {
		if completed[i] {
			errCh <- nil
			continue
		}
		wg.Add(1)
		go func(p partInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := sd.downloadPart(ctx, url, tempDir, p)
			errCh <- err
		}(p)
	}

	go func() {
		wg.Wait()
		close(errCh)
	}()

	// Collect errors
	for err := range errCh {
		if err != nil {
			return fmt.Errorf("download part: %w", err)
		}
	}

	// Merge parts into output file
	if err := sd.mergeParts(tempDir, parts, outputPath); err != nil {
		return fmt.Errorf("merge parts: %w", err)
	}

	return nil
}

// headSize sends a HEAD request to get Content-Length.
func (sd *SplitDownloader) headSize(ctx context.Context, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := sd.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD returned status %d", resp.StatusCode)
	}

	return resp.ContentLength, nil
}

// checkRangeSupport checks if the server supports byte-range requests.
func (sd *SplitDownloader) checkRangeSupport(ctx context.Context, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}

	resp, err := sd.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.Header.Get("Accept-Ranges") == "bytes", nil
}

// splitRanges divides totalSize bytes into roughly equal parts.
func (sd *SplitDownloader) splitRanges(totalSize, numParts int64) []partInfo {
	if numParts <= 0 {
		numParts = 1
	}
	partSize := totalSize / numParts
	if partSize == 0 {
		partSize = totalSize
		numParts = 1
	}

	parts := make([]partInfo, 0, numParts)
	var start int64
	for i := int64(0); i < numParts; i++ {
		end := start + partSize - 1
		if i == numParts-1 {
			end = totalSize - 1 // last part takes remainder
		}
		parts = append(parts, partInfo{
			Index: int(i),
			Start: start,
			End:   end,
		})
		start = end + 1
	}
	return parts
}

// downloadPart downloads one byte-range part and writes it to a temp file.
func (sd *SplitDownloader) downloadPart(ctx context.Context, url, tempDir string, p partInfo) error {
	var lastErr error

	for attempt := 0; attempt <= sd.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}

		// Rate limit
		if sd.limiter != nil {
			expectedBytes := int(p.End - p.Start + 1)
			if err := sd.limiter.Wait(ctx, expectedBytes); err != nil {
				return err
			}
		}

		err := sd.fetchPart(ctx, url, tempDir, p)
		if err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	return fmt.Errorf("part %d failed after %d retries: %w", p.Index, sd.retries, lastErr)
}

// fetchPart does the actual HTTP range request and writes to a temp file.
func (sd *SplitDownloader) fetchPart(ctx context.Context, url, tempDir string, p partInfo) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.Start, p.End))

	resp, err := sd.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	partPath := partTempPath(tempDir, p.Index)
	f, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", partPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(partPath)
		return fmt.Errorf("write part: %w", err)
	}

	return nil
}

// mergeParts concatenates all parts in order into outputPath.
func (sd *SplitDownloader) mergeParts(tempDir string, parts []partInfo, outputPath string) error {
	// Ensure output directory exists
	if dir := filepath.Dir(outputPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	for _, p := range parts {
		partPath := partTempPath(tempDir, p.Index)
		f, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("open part %d: %w", p.Index, err)
		}

		if _, err := io.Copy(out, f); err != nil {
			f.Close()
			return fmt.Errorf("copy part %d: %w", p.Index, err)
		}
		f.Close()
	}

	return nil
}

// singleDownload falls back to a simple single-stream download.
func (sd *SplitDownloader) singleDownload(ctx context.Context, url, outputPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	resp, err := sd.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	if dir := filepath.Dir(outputPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
