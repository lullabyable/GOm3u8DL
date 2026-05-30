package model

import (
	"fmt"
	"net/url"
)

// MediaType represents the type of media stream.
type MediaType string

const (
	MediaTypeVideo     MediaType = "video"
	MediaTypeAudio     MediaType = "audio"
	MediaTypeSubtitles MediaType = "subtitles"
)

// StreamInfo describes a single selectable media stream
// parsed from HLS master playlist, DASH MPD, or MSS manifest.
type StreamInfo struct {
	MediaType     MediaType
	GroupID       string
	Language      string
	Name          string
	Bandwidth     int
	Codecs        string
	Resolution    string  // "1920x1080"
	FrameRate     float64
	Channels      string
	Extension     string
	VideoRange    string // "SDR" / "PQ" / "HLG"
	URL           string
	Playlist      *Playlist
	SegmentsCount int
}

// FormatBandwidth returns a human-readable bandwidth string.
func (s *StreamInfo) FormatBandwidth() string {
	if s.Bandwidth >= 1_000_000 {
		return fmt.Sprintf("%.1f Mbps", float64(s.Bandwidth)/1_000_000)
	}
	return fmt.Sprintf("%.0f Kbps", float64(s.Bandwidth)/1_000)
}

// BaseURL extracts the base URL from the stream URL for resolving relative paths.
func (s *StreamInfo) BaseURL() string {
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	u.Path = dirPath(u.Path)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func dirPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i+1]
		}
	}
	return "/"
}
