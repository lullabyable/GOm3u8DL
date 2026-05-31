package m3u8dl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Concurrency != 8 {
		t.Errorf("ThreadCount = %d, want 8", cfg.Concurrency)
	}
	if cfg.MaxConcurrentTasks != 1 {
		t.Errorf("MaxConcurrentTasks = %d, want 1", cfg.MaxConcurrentTasks)
	}
	if cfg.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3", cfg.RetryCount)
	}
	if cfg.OutputDir != "/downloads" {
		t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/downloads")
	}
	if cfg.Merge != "ts2mp4" {
		t.Errorf("Merge = %q, want %q", cfg.Merge, "ts2mp4")
	}
	if cfg.Headers == nil {
		t.Error("Headers should not be nil")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	data := `{
		"concurrency": 16,
		"max-speed": 1000000,
		"output-dir": "/downloads",
		"merge": "ffmpeg",
		"ffmpeg-path": "/usr/bin/ffmpeg",
		"del-after-done": true,
		"mux-after-done": true,
		"auto-subtitle-fix": true,
		"headers": {
			"User-Agent": "test-agent",
			"Referer": "https://example.com"
		},
		"proxy": "http://127.0.0.1:8080",
		"max-concurrent-tasks": 4,
		"retry-count": 5
	}`
	os.WriteFile(cfgPath, []byte(data), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Concurrency != 16 {
		t.Errorf("ThreadCount = %d, want 16", cfg.Concurrency)
	}
	if cfg.MaxSpeed != 1000000 {
		t.Errorf("MaxSpeed = %d, want 1000000", cfg.MaxSpeed)
	}
	if cfg.OutputDir != "/downloads" {
		t.Errorf("OutputDir = %q, want /downloads", cfg.OutputDir)
	}
	if cfg.Merge != "ffmpeg" {
		t.Errorf("Merge = %q, want %q", cfg.Merge, "ffmpeg")
	}
	if cfg.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("FFmpegPath = %q, want /usr/bin/ffmpeg", cfg.FFmpegPath)
	}
	if !cfg.DelAfterDone {
		t.Error("DelAfterDone should be true")
	}
	if !cfg.MuxAfterDone {
		t.Error("MuxAfterDone should be true")
	}
	if !cfg.AutoSubtitleFix {
		t.Error("AutoSubtitleFix should be true")
	}
	if cfg.Headers["User-Agent"] != "test-agent" {
		t.Errorf("Headers[User-Agent] = %q", cfg.Headers["User-Agent"])
	}
	if cfg.Proxy != "http://127.0.0.1:8080" {
		t.Errorf("Proxy = %q", cfg.Proxy)
	}
	if cfg.MaxConcurrentTasks != 4 {
		t.Errorf("MaxConcurrentTasks = %d, want 4", cfg.MaxConcurrentTasks)
	}
	if cfg.RetryCount != 5 {
		t.Errorf("RetryCount = %d, want 5", cfg.RetryCount)
	}
}

func TestLoadConfigPartial(t *testing.T) {
	// Partial config should fill in defaults for missing fields
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "partial.json")
	os.WriteFile(cfgPath, []byte(`{"concurrency": 4}`), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Concurrency != 4 {
		t.Errorf("ThreadCount = %d, want 4", cfg.Concurrency)
	}
	// Defaults should fill in
	if cfg.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3 (default)", cfg.RetryCount)
	}
	if cfg.MaxConcurrentTasks != 1 {
		t.Errorf("MaxConcurrentTasks = %d, want 1 (default)", cfg.MaxConcurrentTasks)
	}
}

func TestLoadConfigNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	os.WriteFile(cfgPath, []byte(`{invalid json`), 0644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSaveConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "saved.json")

	cfg := DefaultConfig()
	cfg.Concurrency = 12
	cfg.MaxSpeed = 500000
	cfg.Headers = map[string]string{"X-Test": "value"}

	err := SaveConfig(cfgPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded.Concurrency != 12 {
		t.Errorf("ThreadCount = %d, want 12", loaded.Concurrency)
	}
	if loaded.MaxSpeed != 500000 {
		t.Errorf("MaxSpeed = %d, want 500000", loaded.MaxSpeed)
	}
	if loaded.Headers["X-Test"] != "value" {
		t.Errorf("Headers[X-Test] = %q", loaded.Headers["X-Test"])
	}
}

func TestSaveConfigNil(t *testing.T) {
	err := SaveConfig("/tmp/test.json", nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestSaveConfigCreatesDirs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sub", "dir", "config.json")

	err := SaveConfig(cfgPath, DefaultConfig())
	if err != nil {
		t.Fatalf("SaveConfig should create dirs: %v", err)
	}

	if _, err := os.Stat(cfgPath); err != nil {
		t.Error("config file should exist")
	}
}

func TestFindConfig(t *testing.T) {
	// Test priority: current dir > env > xdg
	// Current dir wins over env
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env-config.json")
	os.WriteFile(envPath, []byte(`{}`), 0644)

	t.Setenv("M3U8DL_CONFIG", envPath)

	// With no m3u8dl.json in cwd, env should be found
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	path, found := FindConfig()
	if !found {
		t.Error("FindConfig should find env config")
	}
	if path != envPath {
		t.Errorf("FindConfig path = %q, want %q", path, envPath)
	}
}

func TestFindConfigCurrentDirPriority(t *testing.T) {
	// Current dir config should win over env config
	dir := t.TempDir()
	localPath := filepath.Join(dir, "m3u8dl.json")
	envPath := filepath.Join(dir, "env-config.json")
	os.WriteFile(localPath, []byte(`{}`), 0644)
	os.WriteFile(envPath, []byte(`{}`), 0644)

	t.Setenv("M3U8DL_CONFIG", envPath)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	path, found := FindConfig()
	if !found {
		t.Error("FindConfig should find local config")
	}
	if path != "m3u8dl.json" {
		t.Errorf("FindConfig path = %q, want %q (current dir should win)", path, "m3u8dl.json")
	}
}

func TestFindConfigNotFound(t *testing.T) {
	// Clear env and test from empty dir
	dir := t.TempDir()
	t.Setenv("M3U8DL_CONFIG", filepath.Join(dir, "nonexistent.json"))

	// Change to empty dir to avoid finding ./m3u8dl.json
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	_, found := FindConfig()
	if found {
		t.Error("FindConfig should not find anything in empty dir")
	}
}

func TestApplyToRequest(t *testing.T) {
	cfg := &Config{
		Concurrency:        16,
		MaxSpeed:           1000000,
		OutputDir:          "/downloads",
		Merge:              "ffmpeg",
		FFmpegPath:         "/usr/bin/ffmpeg",
		DelAfterDone:       true,
		MuxAfterDone:       true,
		AutoSubtitleFix:    true,
		Headers:            map[string]string{"User-Agent": "test"},
		RetryCount:         5,
		MaxConcurrentTasks: 4,
	}

	req := &model.DownloadRequest{}
	cfg.ApplyToRequest(req)

	if req.ThreadCount != 16 {
		t.Errorf("ThreadCount = %d, want 16", req.ThreadCount)
	}
	if req.MaxSpeed != 1000000 {
		t.Errorf("MaxSpeed = %d, want 1000000", req.MaxSpeed)
	}
	if req.OutputDir != "/downloads" {
		t.Errorf("OutputDir = %q, want /downloads", req.OutputDir)
	}
	if req.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("FFmpegPath = %q, want /usr/bin/ffmpeg", req.FFmpegPath)
	}
	if req.MergeMode != model.MergeModeFFmpeg {
		t.Errorf("MergeMode = %d, want %d", req.MergeMode, model.MergeModeFFmpeg)
	}
	if !req.DelAfterDone {
		t.Error("DelAfterDone should be true")
	}
	if !req.MuxAfterDone {
		t.Error("MuxAfterDone should be true")
	}
	if !req.AutoSubtitleFix {
		t.Error("AutoSubtitleFix should be true")
	}
	if req.DownloadRetryCount != 5 {
		t.Errorf("DownloadRetryCount = %d, want 5", req.DownloadRetryCount)
	}
	if req.Headers["User-Agent"] != "test" {
		t.Errorf("Headers[User-Agent] = %q", req.Headers["User-Agent"])
	}
}

func TestApplyToRequestHeaderMerge(t *testing.T) {
	cfg := &Config{
		Headers: map[string]string{
			"User-Agent": "config-agent",
			"Referer":    "https://config.com",
		},
	}

	req := &model.DownloadRequest{
		Headers: map[string]string{
			"User-Agent": "request-agent", // should not be overridden
		},
	}

	cfg.ApplyToRequest(req)

	// Request headers take priority
	if req.Headers["User-Agent"] != "request-agent" {
		t.Errorf("User-Agent = %q, want request-agent (request wins)", req.Headers["User-Agent"])
	}
	// Config headers fill in gaps
	if req.Headers["Referer"] != "https://config.com" {
		t.Errorf("Referer = %q, want https://config.com", req.Headers["Referer"])
	}
}

func TestApplyToRequestNilHeaders(t *testing.T) {
	cfg := &Config{
		Concurrency: 4,
		Headers:     map[string]string{"X-Custom": "val"},
	}

	req := &model.DownloadRequest{}
	cfg.ApplyToRequest(req)

	if req.Headers["X-Custom"] != "val" {
		t.Errorf("Headers[X-Custom] = %q", req.Headers["X-Custom"])
	}
}
