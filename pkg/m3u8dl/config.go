package m3u8dl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// Config holds persistent configuration loaded from a JSON file.
type Config struct {
	// Default download settings
	ThreadCount     int   `json:"thread_count"`
	MaxSpeed        int64 `json:"max_speed"`
	OutputDir       string `json:"output_dir"`
	TmpDir          string `json:"tmp_dir"`
	MergeMode       int    `json:"merge_mode"`
	FFmpegPath      string `json:"ffmpeg_path"`
	DelAfterDone    bool   `json:"del_after_done"`
	MuxAfterDone    bool   `json:"mux_after_done"`
	AutoSubtitleFix bool   `json:"auto_subtitle_fix"`

	// HTTP settings
	Headers map[string]string `json:"headers"`
	Proxy   string            `json:"proxy"`

	// Advanced
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
	RetryCount         int `json:"retry_count"`
}

// DefaultConfig returns a Config with sensible default values.
func DefaultConfig() *Config {
	return &Config{
		ThreadCount:        8,
		MaxSpeed:           0,
		OutputDir:          "/downloads",
		MergeMode:          int(model.MergeModeTS2MP4),
		FFmpegPath:         "",
		DelAfterDone:       false,
		MuxAfterDone:       false,
		AutoSubtitleFix:    false,
		Headers:            make(map[string]string),
		Proxy:              "",
		MaxConcurrentTasks: 1,
		RetryCount:         3,
	}
}

// LoadConfig reads a JSON config file from the given path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	return cfg, nil
}

// SaveConfig writes the config as formatted JSON to the given path.
func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}

	return nil
}

// FindConfig searches standard locations for a config file and returns the
// first one found. Search order: ./m3u8dl.json, M3U8DL_CONFIG env, ~/.config/m3u8dl/config.json.
func FindConfig() (string, bool) {
	// 1. Current directory (highest priority for project-level config)
	if _, err := os.Stat("m3u8dl.json"); err == nil {
		return "m3u8dl.json", true
	}

	// 2. Environment variable
	if env := os.Getenv("M3U8DL_CONFIG"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, true
		}
	}

	// 3. XDG config home (lowest priority, user-level default)
	home, err := os.UserHomeDir()
	if err == nil {
		xdgPath := filepath.Join(home, ".config", "m3u8dl", "config.json")
		if _, err := os.Stat(xdgPath); err == nil {
			return xdgPath, true
		}
	}

	return "", false
}

// ApplyToRequest applies the config values to a DownloadRequest.
// Non-zero/non-empty config values override existing request fields.
func (c *Config) ApplyToRequest(req *model.DownloadRequest) {
	if c.ThreadCount > 0 {
		req.ThreadCount = c.ThreadCount
	}
	if c.MaxSpeed > 0 {
		req.MaxSpeed = c.MaxSpeed
	}
	if c.OutputDir != "" {
		req.OutputDir = c.OutputDir
	}
	if c.FFmpegPath != "" {
		req.FFmpegPath = c.FFmpegPath
	}
	req.MergeMode = model.MergeMode(c.MergeMode)
	req.DelAfterDone = c.DelAfterDone
	req.MuxAfterDone = c.MuxAfterDone
	req.AutoSubtitleFix = c.AutoSubtitleFix

	if c.RetryCount > 0 {
		req.DownloadRetryCount = c.RetryCount
	}

	// Merge headers (config headers are base, request headers override)
	if len(c.Headers) > 0 {
		if req.Headers == nil {
			req.Headers = make(map[string]string)
		}
		for k, v := range c.Headers {
			if _, exists := req.Headers[k]; !exists {
				req.Headers[k] = v
			}
		}
	}
}

// ApplyToRequestWithCLI applies config values to a DownloadRequest, but ONLY
// for fields that were NOT explicitly set via CLI flags.
// cliFlags is the set of flag names that were explicitly provided on the command line.
// This ensures CLI always wins over config file.
func (c *Config) ApplyToRequestWithCLI(req *model.DownloadRequest, cliFlags map[string]bool) {
	if !cliFlags["concurrency"] && c.ThreadCount > 0 {
		req.ThreadCount = c.ThreadCount
	}
	if !cliFlags["max-speed"] && c.MaxSpeed > 0 {
		req.MaxSpeed = c.MaxSpeed
	}
	if !cliFlags["o"] && c.OutputDir != "" {
		req.OutputDir = c.OutputDir
	}
	if !cliFlags["ffmpeg-path"] && c.FFmpegPath != "" {
		req.FFmpegPath = c.FFmpegPath
	}
	if !cliFlags["merge"] {
		req.MergeMode = model.MergeMode(c.MergeMode)
	}
	if !cliFlags["del-after-done"] {
		req.DelAfterDone = c.DelAfterDone
	}
	if !cliFlags["mux-after-done"] {
		req.MuxAfterDone = c.MuxAfterDone
	}
	if !cliFlags["auto-subtitle-fix"] {
		req.AutoSubtitleFix = c.AutoSubtitleFix
	}
	if !cliFlags["retry"] && c.RetryCount > 0 {
		req.DownloadRetryCount = c.RetryCount
	}

	// Merge headers: config provides base, CLI headers override
	if len(c.Headers) > 0 {
		if req.Headers == nil {
			req.Headers = make(map[string]string)
		}
		for k, v := range c.Headers {
			if _, exists := req.Headers[k]; !exists {
				req.Headers[k] = v
			}
		}
	}
}

// MergeConfig merges config into CLI options, CLI values always win.
// Returns the merged values. This is a convenience for the CLI layer.
type CLIOptions struct {
	URL         string
	OutputDir   string
	TmpDir      string
	SaveName    string
	Concurrency int
	MaxSpeed    int64
	MergeMode   string
	Headers     map[string]string
	Keys        []string
	AutoSub     bool
	SubOnly     bool
	SVSelect    string
}

// MergeWithConfig applies config defaults for any zero-value CLI fields.
// Non-zero CLI fields are never overwritten.
func (o *CLIOptions) MergeWithConfig(cfg *Config) {
	if o.Concurrency == 0 && cfg.ThreadCount > 0 {
		o.Concurrency = cfg.ThreadCount
	}
	if o.MaxSpeed == 0 && cfg.MaxSpeed > 0 {
		o.MaxSpeed = cfg.MaxSpeed
	}
	if o.OutputDir == "" && cfg.OutputDir != "" {
		o.OutputDir = cfg.OutputDir
	}
	if o.TmpDir == "" && cfg.TmpDir != "" {
		o.TmpDir = cfg.TmpDir
	}
	if o.MergeMode == "" && cfg.MergeMode > 0 {
		switch cfg.MergeMode {
		case 0:
			o.MergeMode = "binary"
		case 1:
			o.MergeMode = "ts2mp4"
		case 2:
			o.MergeMode = "fmp4"
		case 3:
			o.MergeMode = "ffmpeg"
		}
	}
	if !o.AutoSub && cfg.AutoSubtitleFix {
		o.AutoSub = true
	}
	if !o.SubOnly {
		// SubOnly defaults to false, no config equivalent
	}

	// Merge headers: config base, CLI overrides
	if len(cfg.Headers) > 0 {
		if o.Headers == nil {
			o.Headers = make(map[string]string)
		}
		for k, v := range cfg.Headers {
			if _, exists := o.Headers[k]; !exists {
				o.Headers[k] = v
			}
		}
	}
}
