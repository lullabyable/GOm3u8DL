package model

// DownloadResult holds the outcome of a download task.
type DownloadResult struct {
	TaskID     string
	Status     TaskStatus
	OutputPath string
	Duration   float64 // total elapsed seconds
	FileSize   int64
	Error      error
}

// MergeMode controls how downloaded segments are assembled.
type MergeMode int

const (
	MergeModeDefault MergeMode = iota // zero value: FFmpeg (recommended)
	MergeModeTS2MP4                   // experimental pure Go TS→MP4 remux
	MergeModeFMP4                     // experimental pure Go fragmented MP4
	MergeModeFFmpeg                   // recommended FFmpeg stream-copy backend
	MergeModeNo                       // download only, keep segments
	MergeModeBinary                   // explicit raw binary concatenation (not MP4 remux)
)

// DownloadRequest configures a single download operation.
type DownloadRequest struct {
	// Direct stream reference (skip GetStreams).
	Stream *StreamInfo
	// Or URL for engine to parse.
	URL string
	// Auto-selection rule.
	AutoSelect *AutoSelectRule

	// Output config.
	OutputDir string
	SaveName  string
	TmpDir    string // temp directory for downloads, defaults to OutputDir

	// HTTP config.
	Headers map[string]string

	// Download config.
	ThreadCount        int   // segment concurrency
	MaxSpeed           int64 // bytes/sec, 0=unlimited
	DownloadRetryCount int

	// Merge config.
	MergeMode    MergeMode
	FFmpegPath   string // executable/directory for default or FFmpeg mode; empty searches PATH
	MuxAfterDone bool
	DelAfterDone bool

	// Decryption config.
	Keys    []string // kid:key pairs
	KeyFile string

	// DRM decryption binary (optional, pure Go preferred).
	DecryptionBin string

	// Subtitle config.
	AutoSubtitleFix bool
	SubOnly         bool
}

// AutoSelectRule defines criteria for automatic stream selection.
type AutoSelectRule struct {
	MaxResolution  string // "1080p"
	PreferredLang  string // "zh"
	PreferredCodec string // "avc1"
}
