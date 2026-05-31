package m3u8dl

// Options holds global engine configuration.
type Options struct {
	SegmentConcurrency int     // parallel segment downloads per task
	GlobalMaxSpeed     int64   // bytes/sec, 0=unlimited
	TempDir            string  // temp file directory
	FFProbePath        string  // ffprobe binary path
	LogLevel           LogLevel
}

// Option is a functional option for Engine configuration.
type Option func(*Options)

// defaultOptions returns sensible defaults.
func defaultOptions() Options {
	return Options{
		SegmentConcurrency: 8,
		GlobalMaxSpeed:     0,
		TempDir:            "",
		FFProbePath:        "ffprobe",
		LogLevel:           LogInfo,
	}
}

func WithSegmentConcurrency(n int) Option {
	return func(o *Options) { o.SegmentConcurrency = n }
}

func WithGlobalMaxSpeed(bytesPerSec int64) Option {
	return func(o *Options) { o.GlobalMaxSpeed = bytesPerSec }
}

func WithTempDir(dir string) Option {
	return func(o *Options) { o.TempDir = dir }
}

func WithFFProbePath(path string) Option {
	return func(o *Options) { o.FFProbePath = path }
}

func WithLogLevel(level LogLevel) Option {
	return func(o *Options) { o.LogLevel = level }
}
