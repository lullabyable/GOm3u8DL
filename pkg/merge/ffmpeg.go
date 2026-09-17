package merge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FindFFmpeg resolves an executable or directory, then PATH when path is empty.
// It does not launch a shell, download software, or prompt on stdin.
func FindFFmpeg(path string) (string, error) {
	candidate := resolveFFmpegPath(path)
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found (%s): %w; install FFmpeg or set FFmpegPath/-ffmpeg-dir", candidate, err)
	}
	return filepath.Abs(resolved)
}

func resolveFFmpegPath(path string) string {
	if path == "" {
		path = "ffmpeg"
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		for _, name := range []string{"ffmpeg.exe", "ffmpeg"} {
			p := filepath.Join(path, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				abs, err := filepath.Abs(p)
				if err == nil {
					return abs
				}
				return p
			}
		}
	}
	// Explicit relative filenames must not be treated as PATH entries.
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
	}
	return path
}

// Compatibility wrappers. New callers should use RemuxFFmpeg with their task context.
func FFmpegMerge(paths []string, output, ffmpeg string) error {
	return RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: paths}}, output, FFmpegOptions{Path: ffmpeg})
}
func MuxToMP4(input, output, ffmpeg string) error {
	if input == "" {
		return fmt.Errorf("input path is empty")
	}
	return RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: []string{input}}}, output, FFmpegOptions{Path: ffmpeg})
}
func FFmpegMuxAV(video, audio, output, ffmpeg string) error {
	if video == "" || audio == "" {
		return fmt.Errorf("video/audio path is empty")
	}
	return RemuxFFmpeg(context.Background(), []FFmpegInput{{SegmentPaths: []string{video}}, {SegmentPaths: []string{audio}}}, output, FFmpegOptions{Path: ffmpeg})
}
