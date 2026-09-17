package merge

import (
	"strconv"
	"strings"
)

type tailWriter struct {
	data  []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= w.limit {
		w.data = append(w.data[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	overflow := len(w.data) + len(p) - w.limit
	if overflow > 0 {
		w.data = append(w.data[:0], w.data[overflow:]...)
	}
	w.data = append(w.data, p...)
	return n, nil
}

type progressWriter struct {
	pending           string
	duration, outTime float64
	speed             string
	emit              func(FFmpegProgress)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(w.pending[:i])
		w.pending = w.pending[i+1:]
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_us":
			if us, err := strconv.ParseFloat(value, 64); err == nil && us >= 0 {
				w.outTime = us / 1e6
			}
		case "speed":
			w.speed = value
		case "progress":
			percent := -1.0
			if w.duration > 0 {
				percent = w.outTime / w.duration * 100
				if percent > 99.9 {
					percent = 99.9
				}
			}
			if w.emit != nil {
				w.emit(FFmpegProgress{Phase: "remuxing", OutTime: w.outTime, Percent: percent, Speed: w.speed})
			}
		}
	}
	// FFmpeg emits short lines; bound memory even for an unexpected executable.
	if len(w.pending) > 65536 {
		w.pending = ""
	}
	return len(p), nil
}
