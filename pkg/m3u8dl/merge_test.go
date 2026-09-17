package m3u8dl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestDefaultMergeModeAndOutput(t *testing.T) {
	if model.MergeModeDefault != 0 || model.MergeModeFFmpeg != 3 || model.MergeModeNo != 4 || model.MergeModeBinary != 5 {
		t.Fatal("unexpected enum migration")
	}
	if got := buildOutputPath(model.DownloadRequest{SaveName: "movie"}); filepath.Ext(got) != ".mp4" {
		t.Fatal(got)
	}
	if DefaultConfig().Merge != "ffmpeg" {
		t.Fatal("default must use FFmpeg")
	}
}
func TestMergeDownloadedCancellationStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []StatusEvent
	err := New().MergeDownloadedWithFFmpeg(ctx, model.DownloadRequest{}, nil, EventHandlerFunc{OnStatusChangeFn: func(e StatusEvent) { events = append(events, e) }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Status != model.TaskStatusCancelled || !errors.Is(events[1].Error, context.Canceled) {
		t.Fatal(events)
	}
}
func TestMergeCleanupProtectsOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "movie.mp4")
	os.WriteFile(out, []byte("keep"), 0600)
	if err := cleanupMergedTemp(dir, out); err == nil {
		t.Fatal("deleted directory containing output")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

func TestEngineFFmpegDownloadSuccessAndFailure(t *testing.T) {
	exe, err := merge.FindFFmpeg("")
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	ts := filepath.Join(dir, "fixture.ts")
	cmd := exec.Command(exe, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30", "-t", "0.4", "-c:v", "libx264", "-bf", "2", "-f", "mpegts", ts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	good, err := os.ReadFile(ts)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []bool{true, false} {
		name := "failure"
		if ok {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			data := good
			if !ok {
				data = []byte("HTTP 200 but not media")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
			defer server.Close()
			outDir := t.TempDir()
			tmp := filepath.Join(outDir, "segments")
			playlist := &model.Playlist{MediaParts: []model.MediaPart{{MediaSegments: []model.MediaSegment{{URL: server.URL, Duration: .4}, {URL: server.URL, Duration: .4}}}}}
			req := model.DownloadRequest{Stream: &model.StreamInfo{Playlist: playlist}, OutputDir: outDir, TmpDir: tmp, SaveName: "movie", FFmpegPath: exe, DelAfterDone: true}
			var statuses []StatusEvent
			var phases []string
			err := New().Download(context.Background(), req, EventHandlerFunc{OnStatusChangeFn: func(e StatusEvent) { statuses = append(statuses, e) }, OnProgressFn: func(e ProgressEvent) {
				if e.Phase != "" {
					phases = append(phases, e.Phase)
				}
			}})
			out := filepath.Join(outDir, "movie.mp4")
			if ok {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(out); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(tmp); !os.IsNotExist(err) {
					t.Fatal("successful source cleanup failed")
				}
				if statuses[len(statuses)-1].Status != model.TaskStatusDone {
					t.Fatal(statuses)
				}
				if len(phases) == 0 || phases[len(phases)-1] != "done" {
					t.Fatal(phases)
				}
				if data, err := exec.Command(exe, "-v", "error", "-xerror", "-i", out, "-map", "0:v:0", "-f", "null", "-").CombinedOutput(); err != nil {
					t.Fatalf("decode: %v %s", err, data)
				}
			} else {
				if err == nil {
					t.Fatal("accepted invalid media")
				}
				if _, err := os.Stat(filepath.Join(tmp, "seg_0.ts")); err != nil {
					t.Fatal("failed merge removed source", err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("published failed output")
				}
				if statuses[len(statuses)-1].Status != model.TaskStatusFailed || statuses[len(statuses)-1].Error == nil {
					t.Fatal(statuses)
				}
			}
		})
	}
}
