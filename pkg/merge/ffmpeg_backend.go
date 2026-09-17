package merge

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FFmpegInput is one continuous media track/stream. For two inputs the first is
// video and the second audio. Encrypted data must already have been decrypted.
type FFmpegInput struct {
	InitPath     string
	SegmentPaths []string
}

type FFmpegProgress struct {
	Phase   string  // preparing, remuxing, finalizing, done
	OutTime float64 // media seconds, not wall-clock time
	Percent float64 // estimated; -1 when duration is unknown
	Speed   string
}

type FFmpegOptions struct {
	Path     string
	Duration float64 // expected media seconds; zero means unknown
	// Called synchronously; callback must be fast and must not panic.
	OnProgress func(FFmpegProgress)
}

// FFmpegError is suitable for errors.As; Unwrap preserves cancellation/exit errors.
// The diagnostic log is retained on both success and failure, next to the output.
type FFmpegError struct {
	Stage   string
	LogPath string
	Stderr  string // bounded tail, full diagnostics are in LogPath
	Err     error
}

func (e *FFmpegError) Error() string {
	return fmt.Sprintf("ffmpeg %s: %v (log: %s)\n%s", e.Stage, e.Err, e.LogPath, e.Stderr)
}
func (e *FFmpegError) Unwrap() error { return e.Err }

// RemuxFFmpeg copies audio/video packets into MP4 without re-encoding.
// Sources are never removed. Existing final outputs are refused. Work files are
// private to this invocation; only a successful, structurally validated output is
// renamed into place. Cancellation requires restarting the merge, not downloads.
func RemuxFFmpeg(ctx context.Context, inputs []FFmpegInput, output string, opts FFmpegOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(inputs) < 1 || len(inputs) > 2 {
		return fmt.Errorf("expected one media input or separate video/audio inputs")
	}
	for _, in := range inputs {
		if len(in.SegmentPaths) == 0 {
			return fmt.Errorf("no segments to merge")
		}
	}
	if output == "" {
		return fmt.Errorf("output path is empty")
	}
	exe, err := FindFFmpeg(opts.Path)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	// Serialize cooperating tasks targeting the same output (including Windows).
	lock, err := os.OpenFile(output+".merge.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("lock output (another merge may be running): %w", err)
	}
	lock.Close()
	defer os.Remove(output + ".merge.lock")
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("output already exists, choose another name: %s", output)
	}
	work, err := os.MkdirTemp(filepath.Dir(output), ".m3u8dl-merge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	log, err := os.CreateTemp(filepath.Dir(output), filepath.Base(output)+".ffmpeg-*.log")
	if err != nil {
		return err
	}
	defer log.Close()
	tail := &tailWriter{limit: 32768}
	failure := func(stage string, err error) error {
		fmt.Fprintf(log, "\n%s: %v\n", stage, err)
		return &FFmpegError{Stage: stage, LogPath: log.Name(), Stderr: string(tail.data), Err: err}
	}
	emit := func(p FFmpegProgress) {
		if opts.OnProgress != nil {
			opts.OnProgress(p)
		}
	}
	emit(FFmpegProgress{Phase: "preparing", Percent: -1})
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-nostats", "-xerror", "-progress", "pipe:1", "-y"}
	for i, in := range inputs {
		a, err := prepareFFmpegInput(ctx, in, work, i)
		if err != nil {
			return failure("prepare", err)
		}
		args = append(args, a...)
	}
	if len(inputs) == 2 {
		args = append(args, "-map", "0:v:0", "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0:v?", "-map", "0:a?")
	}
	partial := filepath.Join(work, "output.mp4")
	args = append(args, "-c", "copy", "-movflags", "+faststart", "-f", "mp4", partial)
	fmt.Fprintf(log, "executable: %s\nmode: stream copy; input streams: %d\n", exe, len(inputs))
	progress := &progressWriter{duration: opts.Duration, emit: emit}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdout = progress
	cmd.Stderr = io.MultiWriter(log, tail)
	emit(FFmpegProgress{Phase: "remuxing", Percent: -1})
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return failure("remux", err)
	}
	if err := ctx.Err(); err != nil {
		return failure("cancelled", err)
	}
	emit(FFmpegProgress{Phase: "finalizing", Percent: 99.9, OutTime: progress.outTime, Speed: progress.speed})
	if err := validateMP4(ctx, partial); err != nil {
		return failure("validate", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return failure("commit", fmt.Errorf("output appeared during merge; refusing replacement: %s", output))
	}
	if err := ctx.Err(); err != nil {
		return failure("cancelled", err)
	}
	if err := os.Rename(partial, output); err != nil {
		return failure("commit", err)
	}
	fmt.Fprintln(log, "completed")
	emit(FFmpegProgress{Phase: "done", Percent: 100, OutTime: progress.outTime, Speed: progress.speed})
	return nil
}
