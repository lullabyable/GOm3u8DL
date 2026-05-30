package m3u8dl

import "github.com/lullabyable/GOm3u8DL/pkg/model"

// EventHandler is the interface consumers implement to receive all events.
// This is the key decoupling point — no UI framework dependency.
type EventHandler interface {
	OnProgress(event ProgressEvent)
	OnStatusChange(event StatusEvent)
	OnLog(event LogEvent)
	OnStreamInfo(streams []model.StreamInfo)
}

// EventHandlerFunc provides a functional alternative to EventHandler
// so consumers don't need to implement every method.
type EventHandlerFunc struct {
	OnProgressFn    func(ProgressEvent)
	OnStatusChangeFn func(StatusEvent)
	OnLogFn         func(LogEvent)
	OnStreamInfoFn  func([]model.StreamInfo)
}

func (f EventHandlerFunc) OnProgress(e ProgressEvent) {
	if f.OnProgressFn != nil {
		f.OnProgressFn(e)
	}
}

func (f EventHandlerFunc) OnStatusChange(e StatusEvent) {
	if f.OnStatusChangeFn != nil {
		f.OnStatusChangeFn(e)
	}
}

func (f EventHandlerFunc) OnLog(e LogEvent) {
	if f.OnLogFn != nil {
		f.OnLogFn(e)
	}
}

func (f EventHandlerFunc) OnStreamInfo(s []model.StreamInfo) {
	if f.OnStreamInfoFn != nil {
		f.OnStreamInfoFn(s)
	}
}

// ProgressEvent is emitted frequently during download (multiple times per second).
type ProgressEvent struct {
	TaskID       string
	Total        int64   // total bytes
	Downloaded   int64   // bytes downloaded
	Speed        int64   // current speed bytes/sec
	AvgSpeed     int64   // average speed
	Segments     int     // total segments
	SegmentsDone int     // completed segments
	Percent      float64 // 0.0 ~ 100.0
	ETA          float64 // estimated seconds remaining
}

// StatusEvent is emitted when task state changes.
type StatusEvent struct {
	TaskID string
	Status model.TaskStatus
	Error  error
}

// LogLevel controls log verbosity.
type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
)

// LogEvent is emitted for log messages.
type LogEvent struct {
	Level   LogLevel
	Message string
}
