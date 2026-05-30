package model

import (
	"testing"
)

func TestTaskStatusString(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskStatusPending, "pending"},
		{TaskStatusParsing, "parsing"},
		{TaskStatusDownloading, "downloading"},
		{TaskStatusDecrypting, "decrypting"},
		{TaskStatusMerging, "merging"},
		{TaskStatusMuxing, "muxing"},
		{TaskStatusDone, "done"},
		{TaskStatusFailed, "failed"},
		{TaskStatusCancelled, "cancelled"},
		{TaskStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("TaskStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStreamInfoFormatBandwidth(t *testing.T) {
	tests := []struct {
		bandwidth int
		want      string
	}{
		{1_500_000, "1.5 Mbps"},
		{800_000, "800 Kbps"},
		{12_000_000, "12.0 Mbps"},
	}
	for _, tt := range tests {
		s := &StreamInfo{Bandwidth: tt.bandwidth}
		if got := s.FormatBandwidth(); got != tt.want {
			t.Errorf("FormatBandwidth(%d) = %q, want %q", tt.bandwidth, got, tt.want)
		}
	}
}

func TestStreamInfoBaseURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/path/to/master.m3u8", "https://example.com/path/to/"},
		{"https://example.com/master.m3u8?token=abc", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
	}
	for _, tt := range tests {
		s := &StreamInfo{URL: tt.url}
		if got := s.BaseURL(); got != tt.want {
			t.Errorf("BaseURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
