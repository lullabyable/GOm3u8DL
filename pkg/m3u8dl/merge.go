package m3u8dl

import (
	"context"
	"errors"
	"fmt"

	"github.com/lullabyable/GOm3u8DL/pkg/merge"
	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// MergeDownloadedWithFFmpeg can retry a merge without downloading again.
// One input is a complete media stream; two inputs must be video then audio.
// This method explicitly selects FFmpeg regardless of req.MergeMode.
func (e *Engine) MergeDownloadedWithFFmpeg(ctx context.Context, req model.DownloadRequest, inputs []DownloadResult, handler EventHandler) (err error) {
	req.MergeMode = model.MergeModeFFmpeg
	if handler != nil {
		handler.OnStatusChange(StatusEvent{TaskID: req.SaveName, Status: model.TaskStatusMerging})
	}
	defer func() {
		if handler != nil {
			status := model.TaskStatusDone
			if err != nil {
				status = failureStatus(err)
			}
			handler.OnStatusChange(StatusEvent{TaskID: req.SaveName, Status: status, Error: err})
		}
	}()
	if err = runFFmpegMerge(ctx, req, inputs, handler); err != nil {
		return err
	}
	if req.DelAfterDone {
		for _, input := range inputs {
			if input.TempDir != "" {
				if cleanErr := cleanupMergedTemp(input.TempDir, buildOutputPath(req)); cleanErr != nil && handler != nil {
					handler.OnLog(LogEvent{Level: LogWarn, Message: fmt.Sprintf("输出已完成，但清理临时目录失败: %v", cleanErr)})
				}
			}
		}
	}
	return nil
}

func runFFmpegMerge(ctx context.Context, req model.DownloadRequest, inputs []DownloadResult, handler EventHandler) error {
	media := make([]merge.FFmpegInput, len(inputs))
	duration := 0.0
	for i, input := range inputs {
		media[i] = merge.FFmpegInput{InitPath: input.InitPath, SegmentPaths: input.SegmentPaths}
		d := 0.0
		if input.Playlist != nil {
			for _, part := range input.Playlist.MediaParts {
				for _, seg := range part.MediaSegments {
					d += seg.Duration
				}
			}
		}
		if d > duration {
			duration = d
		}
	}
	return merge.RemuxFFmpeg(ctx, media, buildOutputPath(req), merge.FFmpegOptions{
		Path: req.FFmpegPath, Duration: duration,
		OnProgress: func(p merge.FFmpegProgress) {
			if handler != nil {
				handler.OnProgress(ProgressEvent{TaskID: req.SaveName, Phase: p.Phase, Percent: p.Percent, MediaTime: p.OutTime, MediaDuration: duration, MergeSpeed: p.Speed})
			}
		},
	})
}

func failureStatus(err error) model.TaskStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return model.TaskStatusCancelled
	}
	return model.TaskStatusFailed
}
