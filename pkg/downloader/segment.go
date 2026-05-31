package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/crypto"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// SegmentDownloader handles downloading a single media segment.
type SegmentDownloader struct {
	client  *http.Client
	retries int
}

// NewSegmentDownloader creates a new segment downloader.
func NewSegmentDownloader(client *http.Client, retries int) *SegmentDownloader {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if retries <= 0 {
		retries = 3
	}
	return &SegmentDownloader{
		client:  client,
		retries: retries,
	}
}

// DownloadResult holds the result of a single segment download.
type DownloadResult struct {
	Segment  model.MediaSegment
	FilePath string
	Bytes    int64
	Error    error
}

// Download downloads a single segment to the given directory.
// posIndex is the 0-based position index used for filename, ensuring
// correct ordering regardless of the segment's own Index field.
func (sd *SegmentDownloader) Download(ctx context.Context, seg model.MediaSegment, tempDir string, posIndex int) DownloadResult {
	var lastErr error

	for attempt := 0; attempt <= sd.retries; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			select {
			case <-ctx.Done():
				return DownloadResult{Segment: seg, Error: ctx.Err()}
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}

		data, err := sd.fetch(ctx, seg)
		if err != nil {
			lastErr = err
			continue
		}

		// Decrypt if needed
		if seg.EncryptInfo.Method != model.EncryptMethodNone {
			data, err = sd.decrypt(data, seg.EncryptInfo)
			if err != nil {
				lastErr = fmt.Errorf("decrypt: %w", err)
				continue
			}
		}

		// Write to file — use position index for consistent 0-based naming
		filename := fmt.Sprintf("seg_%d.ts", posIndex)
		filePath := filepath.Join(tempDir, filename)
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			lastErr = fmt.Errorf("write file: %w", err)
			continue
		}

		return DownloadResult{
			Segment:  seg,
			FilePath: filePath,
			Bytes:    int64(len(data)),
		}
	}

	return DownloadResult{Segment: seg, Error: fmt.Errorf("download failed after %d retries: %w", sd.retries, lastErr)}
}

// fetch downloads the raw bytes for a segment.
func (sd *SegmentDownloader) fetch(ctx context.Context, seg model.MediaSegment) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, seg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	if seg.StartRange != nil && seg.ExpectLength != nil {
		start := *seg.StartRange
		end := start + *seg.ExpectLength - 1
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}

	resp, err := sd.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return data, nil
}

// decrypt decrypts segment data based on the encryption info.
func (sd *SegmentDownloader) decrypt(data []byte, info model.EncryptInfo) ([]byte, error) {
	switch info.Method {
	case model.EncryptMethodAES128:
		return crypto.DecryptAES128CBC(data, info.Key, info.IV)
	case model.EncryptMethodAES128ECB:
		return crypto.DecryptAES128ECB(data, info.Key)
	case model.EncryptMethodChaCha20:
		return crypto.DecryptChaCha20(data, info.Key, info.IV)
	default:
		return nil, fmt.Errorf("unsupported encryption method: %d", info.Method)
	}
}
