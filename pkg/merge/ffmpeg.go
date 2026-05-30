package merge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FFmpegMerge merges segments using external ffmpeg binary via concat demuxer.
// This is the fallback merge mode for special formats (Dolby Vision, etc.)
// that cannot be handled by pure Go remuxing.
func FFmpegMerge(segmentPaths []string, outputPath string, ffmpegPath string) error {
	if len(segmentPaths) == 0 {
		return fmt.Errorf("no files to merge")
	}

	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	// Verify ffmpeg is available
	if _, err := exec.LookPath(ffmpegPath); err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}

	// Create concat file listing all segments
	concatFile, err := os.CreateTemp("", "m3u8dl-concat-*.txt")
	if err != nil {
		return fmt.Errorf("create temp concat file: %w", err)
	}
	concatPath := concatFile.Name()
	defer os.Remove(concatPath)

	for _, seg := range segmentPaths {
		// Use absolute path for segments to avoid path resolution issues
		absSeg, err := filepath.Abs(seg)
		if err != nil {
			concatFile.Close()
			return fmt.Errorf("resolve path %s: %w", seg, err)
		}
		if _, err := fmt.Fprintf(concatFile, "file '%s'\n", absSeg); err != nil {
			concatFile.Close()
			return fmt.Errorf("write concat entry: %w", err)
		}
	}
	if err := concatFile.Close(); err != nil {
		return fmt.Errorf("close concat file: %w", err)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Run ffmpeg concat
	ctx := context.Background()
	args := []string{
		"-f", "concat",
		"-safe", "0",
		"-i", concatPath,
		"-c", "copy",
		"-y", // overwrite output
		outputPath,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg concat failed: %w", err)
	}

	return nil
}

// MuxToMP4 remuxes a TS file to MP4 using ffmpeg stream copy.
func MuxToMP4(inputPath string, outputPath string, ffmpegPath string) error {
	if inputPath == "" {
		return fmt.Errorf("input path is empty")
	}

	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	if _, err := exec.LookPath(ffmpegPath); err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	ctx := context.Background()
	args := []string{
		"-i", inputPath,
		"-c", "copy",
		"-y",
		outputPath,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg mux failed: %w", err)
	}

	return nil
}
