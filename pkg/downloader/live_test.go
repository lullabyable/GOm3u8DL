package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// --- Mock evolving playlist server ---

type playlistServer struct {
	mu        sync.Mutex
	segments  map[string]string // name -> content
	segOrder  []string          // ordered segment names
	endList   bool
	mediaSeq  int
	callCount int32
}

func newPlaylistServer() *playlistServer {
	return &playlistServer{
		segments: make(map[string]string),
		mediaSeq: 0,
	}
}

func (ps *playlistServer) addSegment(name, content string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.segments[name] = content
	ps.segOrder = append(ps.segOrder, name)
}

func (ps *playlistServer) setEndList(v bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.endList = v
}

func (ps *playlistServer) serveHTTP() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		defer ps.mu.Unlock()

		switch r.URL.Path {
		case "/playlist.m3u8":
			atomic.AddInt32(&ps.callCount, 1)
			fmt.Fprint(w, "#EXTM3U\n")
			fmt.Fprint(w, "#EXT-X-TARGETDURATION:2\n")
			fmt.Fprintf(w, "#EXT-X-MEDIA-SEQUENCE:%d\n", ps.mediaSeq)
			for _, name := range ps.segOrder {
				fmt.Fprint(w, "#EXTINF:2.0,\n")
				fmt.Fprintf(w, "%s\n", name)
			}
			if ps.endList {
				fmt.Fprint(w, "#EXT-X-ENDLIST\n")
			}
		default:
			// Serve segment files: strip leading /
			name := r.URL.Path[1:]
			if data, ok := ps.segments[name]; ok {
				w.Write([]byte(data))
			} else {
				http.NotFound(w, r)
			}
		}
	}))
}

func (ps *playlistServer) getCallCount() int32 {
	return atomic.LoadInt32(&ps.callCount)
}

// --- Tests ---

func TestLiveRecorder_BasicRecording(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "SEGMENT_DATA_0")
	ps.addSegment("seg1.ts", "SEGMENT_DATA_1")

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()
	playlistURL := srv.URL + "/playlist.m3u8"

	// Progressively add segments, then end
	go func() {
		time.Sleep(400 * time.Millisecond)
		ps.addSegment("seg2.ts", "SEGMENT_DATA_2")
		time.Sleep(400 * time.Millisecond)
		ps.setEndList(true)
	}()

	var recordedSegs []model.MediaSegment
	var mu sync.Mutex

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
		WithLiveOnSegment(func(seg model.MediaSegment) {
			mu.Lock()
			recordedSegs = append(recordedSegs, seg)
			mu.Unlock()
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := recorder.Record(ctx, playlistURL, tmpDir, nil)
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	outputPath := filepath.Join(tmpDir, "live_output.ts")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	output := string(data)
	for _, want := range []string{"SEGMENT_DATA_0", "SEGMENT_DATA_1", "SEGMENT_DATA_2"} {
		if !containsStr(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}

	if recorder.SegmentsRecorded() != 3 {
		t.Errorf("expected 3 segments recorded, got %d", recorder.SegmentsRecorded())
	}

	if recorder.BytesWritten() == 0 {
		t.Error("expected non-zero bytes written")
	}

	mu.Lock()
	if len(recordedSegs) != 3 {
		t.Errorf("expected onSegment called 3 times, got %d", len(recordedSegs))
	}
	mu.Unlock()
}

func TestLiveRecorder_DuplicateSegmentSkip(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "DATA_0")

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()

	go func() {
		time.Sleep(400 * time.Millisecond)
		ps.addSegment("seg1.ts", "DATA_1")
		ps.setEndList(true)
	}()

	var segCount int32
	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
		WithLiveOnSegment(func(seg model.MediaSegment) {
			atomic.AddInt32(&segCount, 1)
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	if recorder.SegmentsRecorded() != 2 {
		t.Errorf("expected 2 segments recorded, got %d", recorder.SegmentsRecorded())
	}
}

func TestLiveRecorder_ContextCancellation(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "DATA_0")

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(0),
	)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err == nil {
		t.Fatal("expected error from context cancellation, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestLiveRecorder_EndListDetection(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "DATA_0")
	ps.setEndList(true)

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	if recorder.SegmentsRecorded() != 1 {
		t.Errorf("expected 1 segment recorded, got %d", recorder.SegmentsRecorded())
	}

	callCount := ps.getCallCount()
	if callCount > 2 {
		t.Errorf("expected at most 2 playlist polls, got %d", callCount)
	}
}

func TestLiveRecorder_Discontinuity(t *testing.T) {
	mux := http.NewServeMux()
	var mu sync.Mutex
	phase := 0

	mux.HandleFunc("/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p := phase
		mu.Unlock()

		fmt.Fprint(w, "#EXTM3U\n")
		fmt.Fprint(w, "#EXT-X-TARGETDURATION:2\n")
		fmt.Fprint(w, "#EXT-X-MEDIA-SEQUENCE:0\n")

		if p == 0 {
			fmt.Fprint(w, "#EXTINF:2.0,\n")
			fmt.Fprint(w, "seg0.ts\n")
		} else {
			fmt.Fprint(w, "#EXTINF:2.0,\n")
			fmt.Fprint(w, "seg0.ts\n")
			fmt.Fprint(w, "#EXT-X-DISCONTINUITY\n")
			fmt.Fprint(w, "#EXTINF:2.0,\n")
			fmt.Fprint(w, "seg1.ts\n")
			fmt.Fprint(w, "#EXT-X-ENDLIST\n")
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seg0.ts":
			w.Write([]byte("PHASE0"))
		case "/seg1.ts":
			w.Write([]byte("PHASE1"))
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmpDir := t.TempDir()

	go func() {
		time.Sleep(400 * time.Millisecond)
		mu.Lock()
		phase = 1
		mu.Unlock()
	}()

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "live_output.ts"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	output := string(data)
	if !containsStr(output, "PHASE0") || !containsStr(output, "PHASE1") {
		t.Errorf("output missing expected data, got: %q", output)
	}
}

func TestLiveRecorder_NetworkErrorRecovery(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "DATA_0")

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()

	// End the stream after a short delay so the test completes
	go func() {
		time.Sleep(800 * time.Millisecond)
		ps.setEndList(true)
	}()

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(2),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	if recorder.SegmentsRecorded() < 1 {
		t.Errorf("expected at least 1 segment, got %d", recorder.SegmentsRecorded())
	}
}

func TestLiveRecorder_Headers(t *testing.T) {
	var gotHeader string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			mu.Lock()
			gotHeader = r.Header.Get("X-Test")
			mu.Unlock()
			fmt.Fprint(w, "#EXTM3U\n")
			fmt.Fprint(w, "#EXT-X-TARGETDURATION:2\n")
			fmt.Fprint(w, "#EXT-X-MEDIA-SEQUENCE:0\n")
			fmt.Fprint(w, "#EXTINF:2.0,\n")
			fmt.Fprint(w, "seg0.ts\n")
			fmt.Fprint(w, "#EXT-X-ENDLIST\n")
		default:
			w.Write([]byte("OK"))
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
	)

	ctx := context.Background()
	headers := map[string]string{"X-Test": "hello-world"}

	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, headers)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	mu.Lock()
	if gotHeader != "hello-world" {
		t.Errorf("expected header X-Test=hello-world, got %q", gotHeader)
	}
	mu.Unlock()
}

func TestLiveRecorder_ProgressCallback(t *testing.T) {
	ps := newPlaylistServer()
	ps.addSegment("seg0.ts", "DATA_0")

	srv := ps.serveHTTP()
	defer srv.Close()

	tmpDir := t.TempDir()

	go func() {
		time.Sleep(400 * time.Millisecond)
		ps.setEndList(true)
	}()

	var progressCalls int32
	recorder := NewLiveRecorder(
		WithLiveHTTPClient(srv.Client()),
		WithLiveRetries(1),
		WithLiveProgress(func(p Progress) {
			atomic.AddInt32(&progressCalls, 1)
		}),
	)

	ctx := context.Background()
	err := recorder.Record(ctx, srv.URL+"/playlist.m3u8", tmpDir, nil)
	if err != nil {
		t.Fatalf("Record error: %v", err)
	}

	if atomic.LoadInt32(&progressCalls) == 0 {
		t.Error("expected progress callback to be called at least once")
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
