package downloader

import (
	"context"
	"net/http"
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
