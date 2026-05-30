package mss

import (
	"fmt"
	"testing"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

const testManifest = `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="1200000000">
  <StreamIndex Type="video" Name="video" Chunks="3" QualityLevels="2">
    <QualityLevel Index="0" Bitrate="2000000" FourCC="H264" MaxWidth="1280" MaxHeight="720" />
    <QualityLevel Index="1" Bitrate="4000000" FourCC="H264" MaxWidth="1920" MaxHeight="1080" />
    <c d="20000000" /><c d="20000000" /><c d="20000000" />
  </StreamIndex>
  <StreamIndex Type="audio" Name="audio_eng" Language="en" Chunks="3">
    <QualityLevel Index="0" Bitrate="128000" FourCC="AACL" SamplingRate="44000" Channels="2" />
    <c d="20000000" /><c d="20000000" /><c d="20000000" />
  </StreamIndex>
</SmoothStreamingMedia>`

func TestParseVideoAndAudio(t *testing.T) {
	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(testManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// 2 video quality levels + 1 audio = 3 streams
	if len(streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(streams))
	}

	// Video stream 1 (720p)
	v1 := streams[0]
	if v1.MediaType != model.MediaTypeVideo {
		t.Errorf("v1.MediaType = %s, want video", v1.MediaType)
	}
	if v1.Bandwidth != 2000000 {
		t.Errorf("v1.Bandwidth = %d, want 2000000", v1.Bandwidth)
	}
	if v1.Resolution != "1280x720" {
		t.Errorf("v1.Resolution = %s, want 1280x720", v1.Resolution)
	}
	if v1.Codecs != "avc1" {
		t.Errorf("v1.Codecs = %s, want avc1", v1.Codecs)
	}
	if v1.Name != "video_720p" {
		t.Errorf("v1.Name = %s, want video_720p", v1.Name)
	}

	// Video stream 2 (1080p)
	v2 := streams[1]
	if v2.Bandwidth != 4000000 {
		t.Errorf("v2.Bandwidth = %d, want 4000000", v2.Bandwidth)
	}
	if v2.Resolution != "1920x1080" {
		t.Errorf("v2.Resolution = %s, want 1920x1080", v2.Resolution)
	}

	// Audio stream
	a1 := streams[2]
	if a1.MediaType != model.MediaTypeAudio {
		t.Errorf("a1.MediaType = %s, want audio", a1.MediaType)
	}
	if a1.Bandwidth != 128000 {
		t.Errorf("a1.Bandwidth = %d, want 128000", a1.Bandwidth)
	}
	if a1.Language != "en" {
		t.Errorf("a1.Language = %s, want en", a1.Language)
	}
	if a1.Channels != "2" {
		t.Errorf("a1.Channels = %s, want 2", a1.Channels)
	}
	if a1.Codecs != "mp4a.40.2" {
		t.Errorf("a1.Codecs = %s, want mp4a.40.2", a1.Codecs)
	}
}

func TestParseSegments(t *testing.T) {
	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(testManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	v1 := streams[0]
	if v1.Playlist == nil {
		t.Fatal("v1.Playlist is nil")
	}

	// Total duration should be 120s (1200000000 / 10_000_000)
	if v1.Playlist.TotalDuration != 120.0 {
		t.Errorf("TotalDuration = %f, want 120.0", v1.Playlist.TotalDuration)
	}

	segs := v1.Playlist.MediaParts[0].MediaSegments
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segs))
	}

	// Segment durations: 20000000 / 10_000_000 = 2.0s
	for i, seg := range segs {
		if seg.Duration != 2.0 {
			t.Errorf("seg[%d].Duration = %f, want 2.0", i, seg.Duration)
		}
		if seg.Index != int64(i) {
			t.Errorf("seg[%d].Index = %d, want %d", i, seg.Index, i)
		}
	}
}

func TestSegmentURLConstruction(t *testing.T) {
	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(testManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	v1 := streams[0]
	segs := v1.Playlist.MediaParts[0].MediaSegments

	// URL pattern: {baseURL}/QualityLevels({bitrate})/Fragments({streamName}={timestamp})
	expectedURLs := []string{
		"https://cdn.example.com/manifest.ism/QualityLevels(2000000)/Fragments(video=0)",
		"https://cdn.example.com/manifest.ism/QualityLevels(2000000)/Fragments(video=20000000)",
		"https://cdn.example.com/manifest.ism/QualityLevels(2000000)/Fragments(video=40000000)",
	}

	for i, seg := range segs {
		if seg.URL != expectedURLs[i] {
			t.Errorf("seg[%d].URL = %s, want %s", i, seg.URL, expectedURLs[i])
		}
	}

	// Audio segments
	a1 := streams[2]
	audioSegs := a1.Playlist.MediaParts[0].MediaSegments
	if len(audioSegs) != 3 {
		t.Fatalf("expected 3 audio segments, got %d", len(audioSegs))
	}

	expectedAudioURL := "https://cdn.example.com/manifest.ism/QualityLevels(128000)/Fragments(audio_eng=0)"
	if audioSegs[0].URL != expectedAudioURL {
		t.Errorf("audio seg[0].URL = %s, want %s", audioSegs[0].URL, expectedAudioURL)
	}
}

func TestParseExplicitTimestamps(t *testing.T) {
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="600000000">
  <StreamIndex Type="video" Name="video" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="1000000" FourCC="H264" MaxWidth="640" MaxHeight="360" />
    <c t="0" d="20000000" />
    <c t="20000000" d="20000000" />
    <c t="40000000" d="20000000" />
  </StreamIndex>
</SmoothStreamingMedia>`

	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(manifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	segs := streams[0].Playlist.MediaParts[0].MediaSegments
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segs))
	}

	expectedURLs := []string{
		"https://cdn.example.com/manifest.ism/QualityLevels(1000000)/Fragments(video=0)",
		"https://cdn.example.com/manifest.ism/QualityLevels(1000000)/Fragments(video=20000000)",
		"https://cdn.example.com/manifest.ism/QualityLevels(1000000)/Fragments(video=40000000)",
	}
	for i, seg := range segs {
		if seg.URL != expectedURLs[i] {
			t.Errorf("seg[%d].URL = %s, want %s", i, seg.URL, expectedURLs[i])
		}
	}
}

func TestParseSynthesizedChunks(t *testing.T) {
	// No <c> elements, but Chunks=4 attribute — should synthesize
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="800000000">
  <StreamIndex Type="video" Name="video" Chunks="4" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="1500000" FourCC="H264" MaxWidth="1280" MaxHeight="720" />
  </StreamIndex>
</SmoothStreamingMedia>`

	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(manifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	segs := streams[0].Playlist.MediaParts[0].MediaSegments
	if len(segs) != 4 {
		t.Fatalf("expected 4 segments, got %d", len(segs))
	}

	// Each chunk should be 800000000/4 = 200000000 = 20s
	for i, seg := range segs {
		if seg.Duration != 20.0 {
			t.Errorf("seg[%d].Duration = %f, want 20.0", i, seg.Duration)
		}
	}

	// Check timestamps
	expectedTS := []int64{0, 200000000, 400000000, 600000000}
	for i, seg := range segs {
		expected := "https://cdn.example.com/manifest.ism/QualityLevels(1500000)/Fragments(video=" +
			toString(expectedTS[i]) + ")"
		if seg.URL != expected {
			t.Errorf("seg[%d].URL = %s, want %s", i, seg.URL, expected)
		}
	}
}

func TestParseEmptyManifest(t *testing.T) {
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="0">
</SmoothStreamingMedia>`

	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(manifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 0 {
		t.Fatalf("expected 0 streams, got %d", len(streams))
	}
}

func TestParseInvalidXML(t *testing.T) {
	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	_, err := ext.Parse("not xml at all")
	if err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

func TestParseLiveManifest(t *testing.T) {
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="0" IsLive="true">
  <StreamIndex Type="video" Name="video" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="2000000" FourCC="H264" MaxWidth="1280" MaxHeight="720" />
    <c t="0" d="20000000" />
    <c t="20000000" d="20000000" />
  </StreamIndex>
</SmoothStreamingMedia>`

	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(manifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	if !streams[0].Playlist.IsLive {
		t.Error("expected IsLive=true")
	}
}

func TestParseMultiLanguageAudio(t *testing.T) {
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="0" Duration="600000000">
  <StreamIndex Type="video" Name="video" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="2000000" FourCC="H264" MaxWidth="1280" MaxHeight="720" />
    <c d="20000000" /><c d="20000000" /><c d="20000000" />
  </StreamIndex>
  <StreamIndex Type="audio" Name="audio_en" Language="en" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="128000" FourCC="AACL" SamplingRate="44000" Channels="2" />
    <c d="20000000" /><c d="20000000" /><c d="20000000" />
  </StreamIndex>
  <StreamIndex Type="audio" Name="audio_zh" Language="zh" QualityLevels="1">
    <QualityLevel Index="0" Bitrate="128000" FourCC="AACL" SamplingRate="44000" Channels="2" />
    <c d="20000000" /><c d="20000000" /><c d="20000000" />
  </StreamIndex>
</SmoothStreamingMedia>`

	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(manifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// 1 video + 2 audio = 3 streams
	if len(streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(streams))
	}

	if streams[1].Language != "en" {
		t.Errorf("streams[1].Language = %s, want en", streams[1].Language)
	}
	if streams[2].Language != "zh" {
		t.Errorf("streams[2].Language = %s, want zh", streams[2].Language)
	}
}

func TestFourCCToCodec(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"H264", "avc1"},
		{"AVC1", "avc1"},
		{"H265", "hev1"},
		{"HEVC", "hev1"},
		{"AACL", "mp4a.40.2"},
		{"AAC", "mp4a.40.2"},
		{"EC3", "ec-3"},
		{"AC3", "ac-3"},
		{"OPUS", "opus"},
		{"FLAC", "fLaC"},
		{"AV01", "av01"},
		{"VP09", "vp09"},
		{"XXXX", "xxxx"},
	}
	for _, tt := range tests {
		got := fourCCToCodec(tt.input)
		if got != tt.want {
			t.Errorf("fourCCToCodec(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSegmentCount(t *testing.T) {
	ext := NewExtractor("https://cdn.example.com/manifest.ism")
	streams, err := ext.Parse(testManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, s := range streams {
		if s.Playlist == nil {
			continue
		}
		count := 0
		for _, part := range s.Playlist.MediaParts {
			count += len(part.MediaSegments)
		}
		if count != 3 {
			t.Errorf("stream %s: segment count = %d, want 3", s.Name, count)
		}
	}
}

// toString is a helper to format int64 for URL comparison.
func toString(n int64) string {
	return fmt.Sprintf("%d", n)
}
