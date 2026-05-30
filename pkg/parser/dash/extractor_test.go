package dash

import (
	"testing"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestParseMPDSimple(t *testing.T) {
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT30S" minBufferTime="PT2S">
  <Period id="p0">
    <AdaptationSet id="1" contentType="video" mimeType="video/mp4" segmentAlignment="true">
      <SegmentTemplate timescale="1000" initialization="init.mp4" media="seg_$Number$.m4s" startNumber="1" duration="5000"/>
      <Representation id="v1" bandwidth="2000000" codecs="avc1.640028" width="1920" height="1080" frameRate="30"/>
      <Representation id="v2" bandwidth="800000" codecs="avc1.64001e" width="640" height="360" frameRate="30"/>
    </AdaptationSet>
    <AdaptationSet id="2" contentType="audio" mimeType="audio/mp4" lang="en">
      <SegmentTemplate timescale="1000" initialization="audio_init.mp4" media="audio_$Number$.m4s" startNumber="1" duration="5000"/>
      <Representation id="a1" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="44100"/>
    </AdaptationSet>
  </Period>
</MPD>`

	ext := NewExtractor("https://cdn.example.com/manifest.mpd")
	streams, err := ext.Parse(mpd)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(streams))
	}

	// Video stream 1
	v1 := streams[0]
	if v1.MediaType != model.MediaTypeVideo {
		t.Errorf("v1.MediaType = %s, want video", v1.MediaType)
	}
	if v1.Bandwidth != 2000000 {
		t.Errorf("v1.Bandwidth = %d, want 2000000", v1.Bandwidth)
	}
	if v1.Resolution != "1920x1080" {
		t.Errorf("v1.Resolution = %s, want 1920x1080", v1.Resolution)
	}
	if v1.Codecs != "avc1.640028" {
		t.Errorf("v1.Codecs = %s, want avc1.640028", v1.Codecs)
	}
	if v1.FrameRate != 30 {
		t.Errorf("v1.FrameRate = %f, want 30", v1.FrameRate)
	}

	// Video stream 2
	v2 := streams[1]
	if v2.Bandwidth != 800000 {
		t.Errorf("v2.Bandwidth = %d, want 800000", v2.Bandwidth)
	}
	if v2.Resolution != "640x360" {
		t.Errorf("v2.Resolution = %s, want 640x360", v2.Resolution)
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
}

func TestParseMPDTimeline(t *testing.T) {
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT1M">
  <Period id="p0">
    <AdaptationSet id="1" contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" initialization="init.mp4" media="seg_$Number$.m4s" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="90000" r="11"/>
          <S t="1080000" d="45000"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="3000000" codecs="avc1.640028" width="1920" height="1080"/>
    </AdaptationSet>
  </Period>
</MPD>`

	ext := NewExtractor("https://cdn.example.com/manifest.mpd")
	streams, err := ext.Parse(mpd)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	s := streams[0]
	if s.Playlist == nil {
		t.Fatal("Playlist is nil")
	}

	segments := s.Playlist.MediaParts[0].MediaSegments
	// 12 segments from r=11 (repeat 11 times = 12 segments) + 1 from the last S
	if len(segments) != 13 {
		t.Fatalf("expected 13 segments, got %d", len(segments))
	}

	// Check first segment
	if segments[0].Index != 1 {
		t.Errorf("segments[0].Index = %d, want 1", segments[0].Index)
	}
	if segments[0].Duration != 1.0 { // 90000/90000
		t.Errorf("segments[0].Duration = %f, want 1.0", segments[0].Duration)
	}

	// Check init segment
	if s.Playlist.MediaInit == nil {
		t.Error("expected init segment")
	} else if s.Playlist.MediaInit.URL != "https://cdn.example.com/init.mp4" {
		t.Errorf("init URL = %s", s.Playlist.MediaInit.URL)
	}
}

func TestParseMPDSegmentList(t *testing.T) {
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT15S">
  <Period id="p0">
    <AdaptationSet id="1" contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="1500000" codecs="avc1.640020" width="1280" height="720">
        <SegmentList timescale="1000" duration="5000" startNumber="1">
          <Initialization sourceURL="init.mp4"/>
          <SegmentURL media="seg1.m4s"/>
          <SegmentURL media="seg2.m4s"/>
          <SegmentURL media="seg3.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

	ext := NewExtractor("https://cdn.example.com/manifest.mpd")
	streams, err := ext.Parse(mpd)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	s := streams[0]
	pl := s.Playlist
	if pl == nil {
		t.Fatal("Playlist is nil")
	}

	segs := pl.MediaParts[0].MediaSegments
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segs))
	}

	if segs[0].URL != "https://cdn.example.com/seg1.m4s" {
		t.Errorf("seg[0].URL = %s", segs[0].URL)
	}
	if segs[0].Duration != 5.0 { // 5000/1000
		t.Errorf("seg[0].Duration = %f, want 5.0", segs[0].Duration)
	}

	if pl.MediaInit == nil {
		t.Error("expected init segment")
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"PT30S", 30},
		{"PT1M30S", 90},
		{"PT1H2M3.5S", 3723.5},
		{"PT0S", 0},
		{"", 0},
		{"PT2M", 120},
	}
	for _, tt := range tests {
		got := parseDuration(tt.input)
		if got != tt.want {
			t.Errorf("parseDuration(%q) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestParseFrameRate(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"30", 30},
		{"30000/1001", 29.97002997002997},
		{"24000/1001", 23.976023976023978},
	}
	for _, tt := range tests {
		got, err := parseFrameRate(tt.input)
		if err != nil {
			t.Errorf("parseFrameRate(%q) error: %v", tt.input, err)
			continue
		}
		if diff := got - tt.want; diff > 0.001 || diff < -0.001 {
			t.Errorf("parseFrameRate(%q) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestReplaceTemplate(t *testing.T) {
	tests := []struct {
		tmpl     string
		number   int64
		time     uint64
		timescale int
		want     string
	}{
		{"seg_$Number$.m4s", 5, 0, 1000, "seg_5.m4s"},
		{"seg_$Number$.m4s", 42, 0, 1000, "seg_42.m4s"},
		{"$Time$/segment.mp4", 0, 90000, 90000, "90000/segment.mp4"},
		{"init_$RepresentationID$.mp4", 0, 0, 1000, "init_.mp4"},
	}
	for _, tt := range tests {
		got := replaceTemplate(tt.tmpl, tt.number, tt.time, tt.timescale)
		if got != tt.want {
			t.Errorf("replaceTemplate(%q, %d, %d, %d) = %q, want %q",
				tt.tmpl, tt.number, tt.time, tt.timescale, got, tt.want)
		}
	}
}

func TestResolveURL(t *testing.T) {
	tests := []struct {
		base string
		ref  string
		want string
	}{
		{"https://cdn.example.com/path/manifest.mpd", "seg.m4s", "https://cdn.example.com/path/seg.m4s"},
		{"https://cdn.example.com/path/", "https://other.com/seg.m4s", "https://other.com/seg.m4s"},
		{"https://cdn.example.com/path/manifest.mpd", "/abs/seg.m4s", "https://cdn.example.com/abs/seg.m4s"},
		{"https://cdn.example.com/", "", "https://cdn.example.com/"},
	}
	for _, tt := range tests {
		got := resolveURL(tt.base, tt.ref)
		if got != tt.want {
			t.Errorf("resolveURL(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
		}
	}
}
