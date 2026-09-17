package merge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The test executable acts as a deterministic child process on Windows and Unix.
func init() {
	mode := os.Getenv("GOM3U8DL_TEST_FFMPEG")
	if mode == "" {
		return
	}
	isChild := false
	for _, a := range os.Args[1:] {
		if a == "-progress" {
			isChild = true
		}
	}
	if !isChild {
		return
	}
	out := os.Args[len(os.Args)-1]
	if mode == "cancel" {
		fmt.Print("out_time_us=100000\nprogress=continue\n")
		time.Sleep(30 * time.Second)
		os.Exit(2)
	}
	if mode == "failure" {
		os.WriteFile(out, []byte("partial"), 0600)
		fmt.Fprintln(os.Stderr, "synthetic failure")
		os.Exit(9)
	}
	data := append(testBox("ftyp", []byte("isom0000")), testBox("moov", testBox("free", nil))...)
	data = append(data, testBox("mdat", []byte{1})...)
	if mode == "invalid" {
		data = []byte("not an MP4")
	}
	if err := os.WriteFile(out, data, 0600); err != nil {
		os.Exit(8)
	}
	fmt.Print("out_time_us=1000000\nspeed=2.0x\nprogress=end\n")
	os.Exit(0)
}

func testBox(kind string, body []byte) []byte {
	b := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:8], kind)
	copy(b[8:], body)
	return b
}
func stubInput(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "input.ts")
	if err := os.WriteFile(input, []byte("test input"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return input, filepath.Join(dir, "output.mp4"), exe
}
func TestFFmpegBackendCommitAndProgress(t *testing.T) {
	t.Setenv("GOM3U8DL_TEST_FFMPEG", "success")
	input, out, exe := stubInput(t)
	var events []FFmpegProgress
	err := RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: []string{input}}}, out, FFmpegOptions{Path: exe, Duration: 1, OnProgress: func(p FFmpegProgress) { events = append(events, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMP4(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	for _, p := range events {
		if p.Phase != "done" && p.Percent >= 100 {
			t.Fatalf("premature completion: %+v", p)
		}
	}
	if len(events) == 0 || events[len(events)-1].Phase != "done" {
		t.Fatal(events)
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatal("source removed", err)
	}
	logs, _ := filepath.Glob(out + ".ffmpeg-*.log")
	if len(logs) != 1 {
		t.Fatal(logs)
	}
	works, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".m3u8dl-merge-*"))
	if len(works) != 0 {
		t.Fatal(works)
	}
}
func TestFFmpegBackendFailureAndInvalidOutput(t *testing.T) {
	for _, mode := range []string{"failure", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GOM3U8DL_TEST_FFMPEG", mode)
			input, out, exe := stubInput(t)
			err := RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: []string{input}}}, out, FFmpegOptions{Path: exe})
			var detail *FFmpegError
			if !errors.As(err, &detail) {
				t.Fatalf("expected structured error: %v", err)
			}
			if _, err := os.Stat(detail.LogPath); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("partial published")
			}
			if _, err := os.Stat(input); err != nil {
				t.Fatal("source removed")
			}
			if mode == "failure" && !strings.Contains(detail.Stderr, "synthetic failure") {
				t.Fatal(detail)
			}
		})
	}
}
func TestFFmpegBackendCancellation(t *testing.T) {
	t.Setenv("GOM3U8DL_TEST_FFMPEG", "cancel")
	input, out, exe := stubInput(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := RemuxFFmpeg(ctx, []FFmpegInput{{SegmentPaths: []string{input}}}, out, FFmpegOptions{Path: exe, OnProgress: func(p FFmpegProgress) {
		if p.OutTime > 0 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected task cancellation: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("partial published")
	}
	if _, err := os.Stat(out + ".merge.lock"); !os.IsNotExist(err) {
		t.Fatal("lock leaked")
	}
}
func TestFFmpegBackendRefusesExistingOutput(t *testing.T) {
	t.Setenv("GOM3U8DL_TEST_FFMPEG", "success")
	input, out, exe := stubInput(t)
	os.WriteFile(out, []byte("keep"), 0600)
	if err := RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: []string{input}}}, out, FFmpegOptions{Path: exe}); err == nil {
		t.Fatal("overwrote existing output")
	}
	b, _ := os.ReadFile(out)
	if string(b) != "keep" {
		t.Fatal(string(b))
	}
}
func TestFFmpegInputPreparation(t *testing.T) {
	dir := t.TempDir()
	init := filepath.Join(dir, "init.mp4")
	seg := filepath.Join(dir, "片段 ' one.m4s")
	os.WriteFile(init, []byte("INIT"), 0600)
	os.WriteFile(seg, []byte("MEDIA"), 0600)
	args, err := prepareFFmpegInput(context.Background(), FFmpegInput{InitPath: init, SegmentPaths: []string{seg, seg}}, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(args[len(args)-1])
	if string(data) != "INITMEDIAMEDIA" {
		t.Fatal(string(data))
	}
	args, err = prepareFFmpegInput(context.Background(), FFmpegInput{SegmentPaths: []string{seg, seg}}, dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(args[len(args)-1])
	if !strings.Contains(string(data), "'\\''") {
		t.Fatal("apostrophe not escaped", string(data))
	}
	os.WriteFile(seg, testBox("moof", nil), 0600)
	if _, err := prepareFFmpegInput(context.Background(), FFmpegInput{SegmentPaths: []string{seg}}, dir, 2); err == nil {
		t.Fatal("accepted missing init")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyInputs(ctx, filepath.Join(dir, "cancelled"), []string{init}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestFFmpegProgressPartialWritesAndBoundedTail(t *testing.T) {
	var got []FFmpegProgress
	w := progressWriter{duration: 2, emit: func(p FFmpegProgress) { got = append(got, p) }}
	w.Write([]byte("out_time_us=100"))
	w.Write([]byte("0000\nspeed=3x\nprogress=continue\n"))
	want := FFmpegProgress{Phase: "remuxing", OutTime: 1, Percent: 50, Speed: "3x"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatal(got)
	}
	tail := tailWriter{limit: 4}
	tail.Write([]byte("123"))
	tail.Write([]byte("456"))
	if string(tail.data) != "3456" {
		t.Fatal(string(tail.data))
	}
}

func runMediaCommand(t *testing.T, exe string, args ...string) {
	t.Helper()
	for i := range args {
		args[i] = filepath.ToSlash(args[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if data, err := exec.CommandContext(ctx, exe, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture/verification: %v\n%s", err, data)
	}
}

// Real FFmpeg integration: separate HLS fMP4 tracks, multiple fragments, unicode
// and apostrophes in paths. Decoder validation catches missing init/track mapping.
func TestFFmpegBackendRealFMP4SeparateTracks(t *testing.T) {
	exe, err := FindFFmpeg("")
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(t.TempDir(), "电影 ' sample")
	os.MkdirAll(dir, 0755)
	inputs := make([]FFmpegInput, 2)
	for i, name := range []string{"video", "audio"} {
		sub := filepath.Join(dir, name)
		os.MkdirAll(sub, 0755)
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi"}
		if i == 0 {
			args = append(args, "-i", "testsrc2=size=160x90:rate=24", "-c:v", "libx264", "-g", "24", "-bf", "2", "-an")
		} else {
			args = append(args, "-i", "sine=frequency=440:sample_rate=48000", "-c:a", "aac", "-vn")
		}
		args = append(args, "-t", "2.2", "-f", "hls", "-hls_time", "1", "-hls_playlist_type", "vod", "-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4", "-hls_segment_filename", filepath.Join(sub, "seg%03d.m4s"), filepath.Join(sub, "index.m3u8"))
		runMediaCommand(t, exe, args...)
		segs, _ := filepath.Glob(filepath.Join(sub, "seg*.m4s"))
		if len(segs) < 2 {
			t.Fatal("expected multiple fragments", segs)
		}
		inputs[i] = FFmpegInput{InitPath: filepath.Join(sub, "init.mp4"), SegmentPaths: segs}
	}
	out := filepath.Join(dir, "combined.mp4")
	if err := RemuxFFmpeg(context.Background(), inputs, out, FFmpegOptions{Path: exe, Duration: 2.2}); err != nil {
		t.Fatal(err)
	}
	runMediaCommand(t, exe, "-v", "error", "-xerror", "-i", out, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
	// Single media track path exercises the engine's one-input fMP4 route too.
	single := filepath.Join(dir, "video-only.mp4")
	if err := RemuxFFmpeg(context.Background(), inputs[:1], single, FFmpegOptions{Path: exe}); err != nil {
		t.Fatal(err)
	}
	runMediaCommand(t, exe, "-v", "error", "-xerror", "-i", single, "-map", "0:v:0", "-f", "null", "-")
}
