package merge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func skipIfNoFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found")
	}
}

func TestFFmpegMerge(t *testing.T) {
	skipIfNoFFmpeg(t)

	dir := t.TempDir()

	// Create minimal TS segments using ffmpeg's testsrc
	seg1 := filepath.Join(dir, "seg_0.ts")
	seg2 := filepath.Join(dir, "seg_1.ts")
	out := filepath.Join(dir, "output.mp4")

	// Generate two small TS test files
	for _, seg := range []string{seg1, seg2} {
		cmd := exec.Command("ffmpeg",
			"-f", "lavfi", "-i", "color=c=red:s=320x240:d=0.1",
			"-f", "lavfi", "-i", "sine=f=440:d=0.1",
			"-c:v", "libx264", "-c:a", "aac",
			"-y", seg,
		)
		if err := cmd.Run(); err != nil {
			t.Fatalf("generate test segment: %v", err)
		}
	}

	err := FFmpegMerge([]string{seg1, seg2}, out, "")
	if err != nil {
		t.Fatalf("FFmpegMerge: %v", err)
	}

	// Verify output exists and has content
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("output stat: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}
}

func TestFFmpegMergeEmpty(t *testing.T) {
	err := FFmpegMerge(nil, "/tmp/out.mp4", "ffmpeg")
	if err == nil {
		t.Error("expected error for empty segment list")
	}
}

func TestFFmpegMergeBadPath(t *testing.T) {
	err := FFmpegMerge([]string{"seg.ts"}, "/tmp/out.mp4", "/nonexistent/ffmpeg")
	if err == nil {
		t.Error("expected error for invalid ffmpeg path")
	}
}

func TestMuxToMP4(t *testing.T) {
	skipIfNoFFmpeg(t)

	dir := t.TempDir()

	// Generate a small TS file
	tsFile := filepath.Join(dir, "input.ts")
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "color=c=blue:s=320x240:d=0.1",
		"-f", "lavfi", "-i", "sine=f=440:d=0.1",
		"-c:v", "libx264", "-c:a", "aac",
		"-y", tsFile,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate test TS: %v", err)
	}

	out := filepath.Join(dir, "output.mp4")
	err := MuxToMP4(tsFile, out, "")
	if err != nil {
		t.Fatalf("MuxToMP4: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("output stat: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}
}

func TestMuxToMP4EmptyInput(t *testing.T) {
	err := MuxToMP4("", "/tmp/out.mp4", "ffmpeg")
	if err == nil {
		t.Error("expected error for empty input path")
	}
}

func TestMuxToMP4BadPath(t *testing.T) {
	err := MuxToMP4("/tmp/input.ts", "/tmp/out.mp4", "/nonexistent/ffmpeg")
	if err == nil {
		t.Error("expected error for invalid ffmpeg path")
	}
}
