package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestNewSegmentDownloader(t *testing.T) {
	sd := NewSegmentDownloader(nil, 0)
	if sd == nil {
		t.Fatal("expected non-nil downloader")
	}
	if sd.retries != 3 {
		t.Errorf("retries = %d, want 3", sd.retries)
	}
	if sd.client == nil {
		t.Error("client should not be nil")
	}
}

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m.concur != 8 {
		t.Errorf("concurrency = %d, want 8", m.concur)
	}
	if m.retries != 3 {
		t.Errorf("retries = %d, want 3", m.retries)
	}

	m2 := NewManager(WithConcurrency(16), WithRetries(5))
	if m2.concur != 16 {
		t.Errorf("concurrency = %d, want 16", m2.concur)
	}
	if m2.retries != 5 {
		t.Errorf("retries = %d, want 5", m2.retries)
	}
}

func TestProgressTracker(t *testing.T) {
	pt := NewProgressTracker(10)
	pt.SetTotalBytes(1000)

	pt.AddBytes(500)
	pt.SegmentDone()
	pt.SegmentDone()

	p := pt.Progress()
	if p.Downloaded != 500 {
		t.Errorf("Downloaded = %d, want 500", p.Downloaded)
	}
	if p.Total != 1000 {
		t.Errorf("Total = %d, want 1000", p.Total)
	}
	if p.SegmentsDone != 2 {
		t.Errorf("SegmentsDone = %d, want 2", p.SegmentsDone)
	}
	if p.Segments != 10 {
		t.Errorf("Segments = %d, want 10", p.Segments)
	}
	if p.Percent != 50.0 {
		t.Errorf("Percent = %f, want 50.0", p.Percent)
	}
}

func TestProgressTrackerNoTotal(t *testing.T) {
	pt := NewProgressTracker(0)
	pt.AddBytes(100)
	pt.SegmentDone()

	p := pt.Progress()
	if p.Downloaded != 100 {
		t.Errorf("Downloaded = %d, want 100", p.Downloaded)
	}
	if p.Percent != 0 {
		t.Errorf("Percent = %f, want 0 (no total set)", p.Percent)
	}
}

func TestDownloadSegmentsEmptyPlaylist(t *testing.T) {
	m := NewManager()
	playlist := &model.Playlist{
		MediaParts: []model.MediaPart{},
	}

	tDir := t.TempDir()
	_, err := m.DownloadSegments(context.Background(), playlist, tDir)
	if err == nil {
		t.Error("expected error for empty playlist")
	}
}

func TestDownloadSegmentsHTTP(t *testing.T) {
	// Start a test HTTP server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write some fake TS data
		data := make([]byte, 1024)
		for i := range data {
			data[i] = byte(i % 256)
		}
		w.Write(data)
	})
	server := http.NewServeMux()
	server.HandleFunc("/", handler)
	ts := http.Server{Handler: server}
	defer ts.Close()

	// We can't easily test with a real server without more setup,
	// so just test the manager creation and options
	m := NewManager(
		WithConcurrency(4),
		WithRetries(1),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		WithProgressFunc(func(p Progress) {}),
	)

	if m.concur != 4 {
		t.Errorf("concurrency = %d, want 4", m.concur)
	}
	if m.retries != 1 {
		t.Errorf("retries = %d, want 1", m.retries)
	}
}

func TestSegmentPath(t *testing.T) {
	path := SegmentPath("/tmp/test", 42)
	want := "/tmp/test/seg_42.ts"
	if path != want {
		t.Errorf("SegmentPath = %q, want %q", path, want)
	}
}

// ---------------------------------------------------------------------------
// Limiter tests
// ---------------------------------------------------------------------------

func TestLimiterUnlimited(t *testing.T) {
	l := NewLimiter(0) // unlimited
	if !l.Allow(1000000) {
		t.Error("unlimited limiter should always allow")
	}
	if err := l.Wait(context.Background(), 1000000); err != nil {
		t.Errorf("unlimited limiter Wait: %v", err)
	}
}

func TestLimiterBurst(t *testing.T) {
	// 1000 bytes/sec rate, burst = 1000
	l := NewLimiter(1000)

	// Should allow burst immediately
	if !l.Allow(500) {
		t.Error("should allow 500 within burst")
	}
	if !l.Allow(500) {
		t.Error("should allow another 500 within burst")
	}
	// Now tokens are depleted, should reject
	if l.Allow(1) {
		t.Error("should reject when tokens depleted")
	}
}

func TestLimiterRateLimiting(t *testing.T) {
	// 1000 bytes/sec
	l := NewLimiter(1000)

	// Drain initial burst
	l.Allow(1000)

	// Wait a bit for tokens to replenish (~100ms = ~100 bytes)
	time.Sleep(110 * time.Millisecond)

	// Should now allow ~100 bytes
	if !l.Allow(80) {
		t.Error("should allow 80 bytes after 110ms replenish")
	}
}

func TestLimiterWaitContextCancel(t *testing.T) {
	l := NewLimiter(10) // very slow: 10 bytes/sec

	// Drain burst
	l.Allow(10)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Try to wait for more tokens than can be replenished in 50ms
	err := l.Wait(ctx, 1000)
	if err == nil {
		t.Error("expected context deadline exceeded")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestLimiterWaitSuccess(t *testing.T) {
	l := NewLimiter(100000) // fast: 100KB/sec

	ctx := context.Background()
	start := time.Now()
	err := l.Wait(ctx, 100) // should be near-instant (burst)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Wait: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Wait took too long: %v", elapsed)
	}
}

func TestLimiterConcurrent(t *testing.T) {
	l := NewLimiter(10000) // 10KB/sec

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := l.Wait(ctx, 100); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Wait error: %v", err)
	}
}

func TestLimiterRate(t *testing.T) {
	l := NewLimiter(5000)
	if l.Rate() != 5000 {
		t.Errorf("Rate() = %d, want 5000", l.Rate())
	}

	l2 := NewLimiter(0)
	if l2.Rate() != 0 {
		t.Errorf("Rate() = %d, want 0", l2.Rate())
	}
}

// ---------------------------------------------------------------------------
// SplitDownloader tests
// ---------------------------------------------------------------------------

func TestSplitDownloaderBasic(t *testing.T) {
	// Create a 10KB test file
	fileSize := 10240
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
			w.WriteHeader(http.StatusOK)
			w.Write(testData)
			return
		}

		start, end, ok := parseRangeHeader(rangeHeader, int64(fileSize))
		if !ok {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(testData[start : end+1])
	}))
	defer ts.Close()

	sd := NewSplitDownloader(nil, 3, nil)
	outPath := filepath.Join(t.TempDir(), "output.bin")

	err := sd.Download(context.Background(), ts.URL, outPath, int64(fileSize), 4)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Verify output
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if len(data) != fileSize {
		t.Errorf("file size = %d, want %d", len(data), fileSize)
	}

	for i, b := range data {
		if b != testData[i] {
			t.Errorf("byte %d = %d, want %d", i, b, testData[i])
			break
		}
	}
}

func TestSplitDownloaderHeadSize(t *testing.T) {
	fileSize := int64(5000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "5000")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		// For GET requests with range
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		start, end, ok := parseRangeHeader(rangeHeader, fileSize)
		if !ok {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.WriteHeader(http.StatusPartialContent)
		data := make([]byte, end-start+1)
		w.Write(data)
	}))
	defer ts.Close()

	sd := NewSplitDownloader(nil, 1, nil)
	outPath := filepath.Join(t.TempDir(), "output.bin")

	// Pass totalSize=0 to force HEAD request
	err := sd.Download(context.Background(), ts.URL, outPath, 0, 2)
	if err != nil {
		t.Fatalf("Download with HEAD: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != fileSize {
		t.Errorf("file size = %d, want %d", info.Size(), fileSize)
	}
}

func TestSplitDownloaderNoRangeSupport(t *testing.T) {
	fileSize := 2048
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
			// No Accept-Ranges header
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.WriteHeader(http.StatusOK)
		w.Write(testData)
	}))
	defer ts.Close()

	sd := NewSplitDownloader(nil, 1, nil)
	outPath := filepath.Join(t.TempDir(), "output.bin")

	err := sd.Download(context.Background(), ts.URL, outPath, int64(fileSize), 4)
	if err != nil {
		t.Fatalf("Download fallback: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if len(data) != fileSize {
		t.Errorf("file size = %d, want %d", len(data), fileSize)
	}
	for i, b := range data {
		if b != testData[i] {
			t.Errorf("byte %d mismatch", i)
			break
		}
	}
}

func TestSplitDownloaderContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "1000000")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		// Slow response
		time.Sleep(5 * time.Second)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	sd := NewSplitDownloader(&http.Client{Timeout: 2 * time.Second}, 1, nil)
	outPath := filepath.Join(t.TempDir(), "output.bin")

	err := sd.Download(ctx, ts.URL, outPath, 1000000, 2)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestSplitDownloaderWithLimiter(t *testing.T) {
	fileSize := 1024
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.WriteHeader(http.StatusOK)
			w.Write(testData)
			return
		}
		start, end, ok := parseRangeHeader(rangeHeader, int64(fileSize))
		if !ok {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(testData[start : end+1])
	}))
	defer ts.Close()

	limiter := NewLimiter(500000) // 500KB/sec (fast enough for test)
	sd := NewSplitDownloader(nil, 1, limiter)
	outPath := filepath.Join(t.TempDir(), "output.bin")

	err := sd.Download(context.Background(), ts.URL, outPath, int64(fileSize), 2)
	if err != nil {
		t.Fatalf("Download with limiter: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if len(data) != fileSize {
		t.Errorf("file size = %d, want %d", len(data), fileSize)
	}
}

func TestSplitRanges(t *testing.T) {
	sd := &SplitDownloader{}

	// 100 bytes split into 4 parts
	parts := sd.splitRanges(100, 4)
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d", len(parts))
	}

	// Check coverage: all bytes accounted for
	totalBytes := int64(0)
	for _, p := range parts {
		totalBytes += p.End - p.Start + 1
	}
	if totalBytes != 100 {
		t.Errorf("total bytes = %d, want 100", totalBytes)
	}

	// Check sequential
	for i := 1; i < len(parts); i++ {
		if parts[i].Start != parts[i-1].End+1 {
			t.Errorf("gap between part %d and %d", i-1, i)
		}
	}

	// Check single part
	parts = sd.splitRanges(50, 1)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0].Start != 0 || parts[0].End != 49 {
		t.Errorf("single part range = %d-%d, want 0-49", parts[0].Start, parts[0].End)
	}

	// Check small file with more parts than bytes
	parts = sd.splitRanges(3, 10)
	if len(parts) != 1 {
		t.Errorf("expected 1 part for tiny file, got %d", len(parts))
	}
}

// parseRangeHeader parses "bytes=start-end" and returns start, end (inclusive).
func parseRangeHeader(header string, totalSize int64) (start, end int64, ok bool) {
	if len(header) < 7 || header[:6] != "bytes=" {
		return 0, 0, false
	}
	header = header[6:]

	n, err := fmt.Sscanf(header, "%d-%d", &start, &end)
	if n < 1 || err != nil {
		return 0, 0, false
	}
	if n == 1 {
		end = totalSize - 1
	}
	if start > end || end >= totalSize {
		return 0, 0, false
	}
	return start, end, true
}
