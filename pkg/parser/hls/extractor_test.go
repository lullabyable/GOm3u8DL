package hls

import (
	"testing"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestParseMasterPlaylist(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.64001e,mp4a.40.2"
low/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.640020,mp4a.40.2"
mid/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
high/playlist.m3u8
`

	ext := NewExtractor("https://example.com/master.m3u8")
	streams, playlist, err := ext.Parse(body)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if playlist != nil {
		t.Fatal("expected master playlist, got media playlist")
	}
	if len(streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(streams))
	}

	tests := []struct {
		index      int
		bandwidth  int
		resolution string
		codecs     string
		url        string
	}{
		{0, 800000, "640x360", `avc1.64001e,mp4a.40.2`, "https://example.com/low/playlist.m3u8"},
		{1, 2000000, "1280x720", `avc1.640020,mp4a.40.2`, "https://example.com/mid/playlist.m3u8"},
		{2, 5000000, "1920x1080", `avc1.640028,mp4a.40.2`, "https://example.com/high/playlist.m3u8"},
	}

	for _, tt := range tests {
		s := streams[tt.index]
		if s.Bandwidth != tt.bandwidth {
			t.Errorf("stream[%d].Bandwidth = %d, want %d", tt.index, s.Bandwidth, tt.bandwidth)
		}
		if s.Resolution != tt.resolution {
			t.Errorf("stream[%d].Resolution = %q, want %q", tt.index, s.Resolution, tt.resolution)
		}
		if s.Codecs != tt.codecs {
			t.Errorf("stream[%d].Codecs = %q, want %q", tt.index, s.Codecs, tt.codecs)
		}
		if s.URL != tt.url {
			t.Errorf("stream[%d].URL = %q, want %q", tt.index, s.URL, tt.url)
		}
	}
}

func TestParseMediaPlaylist(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-KEY:METHOD=AES-128,URI="https://example.com/key.bin",IV=0x00000000000000000000000000000001
#EXTINF:9.009,
segment0.ts
#EXTINF:9.009,
segment1.ts
#EXTINF:3.003,
segment2.ts
#EXT-X-ENDLIST
`

	ext := NewExtractor("https://example.com/low/playlist.m3u8")
	streams, playlist, err := ext.Parse(body)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if streams != nil {
		t.Fatal("expected media playlist, got master playlist")
	}
	if playlist == nil {
		t.Fatal("playlist is nil")
	}

	// Check segments
	segments := playlist.MediaParts[0].MediaSegments
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}

	// Check duration
	if playlist.TotalDuration < 21.0 || playlist.TotalDuration > 21.1 {
		t.Errorf("TotalDuration = %f, want ~21.021", playlist.TotalDuration)
	}

	// Check first segment
	seg := segments[0]
	if seg.Duration != 9.009 {
		t.Errorf("segment[0].Duration = %f, want 9.009", seg.Duration)
	}
	if seg.URL != "https://example.com/low/segment0.ts" {
		t.Errorf("segment[0].URL = %q, want https://example.com/low/segment0.ts", seg.URL)
	}
	if seg.EncryptInfo.Method != model.EncryptMethodAES128 {
		t.Errorf("segment[0].EncryptInfo.Method = %d, want AES128", seg.EncryptInfo.Method)
	}
	if seg.EncryptInfo.KeyURL != "https://example.com/key.bin" {
		t.Errorf("segment[0].EncryptInfo.KeyURL = %q", seg.EncryptInfo.KeyURL)
	}

	// Check VOD flag
	if playlist.IsLive {
		t.Error("expected VOD playlist, got live")
	}

	// Check target duration
	if playlist.TargetDuration == nil || *playlist.TargetDuration != 10 {
		t.Errorf("TargetDuration = %v, want 10", playlist.TargetDuration)
	}
}

func TestParseMediaPlaylistLive(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:8
#EXT-X-MEDIA-SEQUENCE:42
#EXTINF:7.5,
segment42.ts
#EXTINF:8.0,
segment43.ts
`

	ext := NewExtractor("https://example.com/live/playlist.m3u8")
	_, playlist, err := ext.Parse(body)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !playlist.IsLive {
		t.Error("expected live playlist, got VOD")
	}
	if playlist.RefreshInterval != 8000 {
		t.Errorf("RefreshInterval = %f, want 8000", playlist.RefreshInterval)
	}
}

func TestParseAttributes(t *testing.T) {
	tests := []struct {
		line string
		key  string
		want string
	}{
		{
			`#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.64001e,mp4a.40.2"`,
			"CODECS",
			"avc1.64001e,mp4a.40.2",
		},
		{
			`#EXT-X-KEY:METHOD=AES-128,URI="https://example.com/key.bin",IV=0x0001`,
			"METHOD",
			"AES-128",
		},
		{
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="English",URI="audio/eng.m3u8"`,
			"TYPE",
			"AUDIO",
		},
	}

	for _, tt := range tests {
		attrs, err := parseAttributes(tt.line)
		if err != nil {
			t.Errorf("parseAttributes(%q) error: %v", tt.line, err)
			continue
		}
		if got := attrs[tt.key]; got != tt.want {
			t.Errorf("parseAttributes(%q)[%q] = %q, want %q", tt.line, tt.key, got, tt.want)
		}
	}
}

func TestResolveURL(t *testing.T) {
	tests := []struct {
		base string
		ref  string
		want string
	}{
		{"https://example.com/path/", "segment.ts", "https://example.com/path/segment.ts"},
		{"https://example.com/path/", "../segment.ts", "https://example.com/segment.ts"},
		{"https://example.com/path/", "https://cdn.com/segment.ts", "https://cdn.com/segment.ts"},
		{"https://example.com/a/b.m3u8", "segment.ts", "https://example.com/a/segment.ts"},
	}
	for _, tt := range tests {
		if got := resolveURL(tt.base, tt.ref); got != tt.want {
			t.Errorf("resolveURL(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
		}
	}
}

func TestParseByteRange(t *testing.T) {
	offset, length := parseByteRange("1024@0")
	if length != 1024 || offset != 0 {
		t.Errorf("parseByteRange(\"1024@0\") = (%d, %d), want (0, 1024)", offset, length)
	}

	offset, length = parseByteRange("512@100")
	if length != 512 || offset != 100 {
		t.Errorf("parseByteRange(\"512@100\") = (%d, %d), want (100, 512)", offset, length)
	}
}

func TestParseMediaPlaylistNoIV(t *testing.T) {
	// RFC 8216 Section 4.3.2.4: If IV is not present, use media sequence number
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:5
#EXT-X-KEY:METHOD=AES-128,URI="https://example.com/key.bin"
#EXTINF:10.0,
segment5.ts
#EXTINF:10.0,
segment6.ts
#EXT-X-ENDLIST
`

	ext := NewExtractor("https://example.com/playlist.m3u8")
	_, playlist, err := ext.Parse(body)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	segments := playlist.MediaParts[0].MediaSegments
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}

	// Segment 5 (media sequence 5) should have IV = 0x00000000000000000000000000000005
	seg5 := segments[0]
	if seg5.EncryptInfo.Method != model.EncryptMethodAES128 {
		t.Errorf("segment[0].EncryptInfo.Method = %d, want AES128", seg5.EncryptInfo.Method)
	}
	if len(seg5.EncryptInfo.IV) != 16 {
		t.Fatalf("segment[0].EncryptInfo.IV length = %d, want 16", len(seg5.EncryptInfo.IV))
	}
	expectedIV5 := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5}
	for i, b := range seg5.EncryptInfo.IV {
		if b != expectedIV5[i] {
			t.Errorf("segment[0].EncryptInfo.IV[%d] = %d, want %d", i, b, expectedIV5[i])
		}
	}

	// Segment 6 (media sequence 6) should have IV = 0x00000000000000000000000000000006
	seg6 := segments[1]
	expectedIV6 := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 6}
	for i, b := range seg6.EncryptInfo.IV {
		if b != expectedIV6[i] {
			t.Errorf("segment[1].EncryptInfo.IV[%d] = %d, want %d", i, b, expectedIV6[i])
		}
	}
}
