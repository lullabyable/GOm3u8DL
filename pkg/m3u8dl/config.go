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
		OutputDir:          ".",
		MergeMode:          int(model.MergeModeBinary),
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
// first one found. Search order: M3U8DL_CONFIG env, ~/.config/m3u8dl/config.json, ./m3u8dl.json.
func FindConfig() (string, bool) {
	// 1. Environment variable
	if env := os.Getenv("M3U8DL_CONFIG"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, true
		}
	}

	// 2. XDG config home
	home, err := os.UserHomeDir()
	if err == nil {
		xdgPath := filepath.Join(home, ".config", "m3u8dl", "config.json")
		if _, err := os.Stat(xdgPath); err == nil {
			return xdgPath, true
		}
	}

	// 3. Current directory
	if _, err := os.Stat("m3u8dl.json"); err == nil {
		return "m3u8dl.json", true
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
