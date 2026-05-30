package subtitle

import (
	"testing"
	"time"
)

func TestParseWebVTT(t *testing.T) {
	body := `WEBVTT

00:00:01.000 --> 00:00:04.000
Hello, world!

00:00:05.000 --> 00:00:08.000
This is a test subtitle.

00:00:10.000 --> 00:00:15.000
Third line with
multiple lines.
`

	vtt, err := ParseWebVTT(body)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}

	if len(vtt.Cues) != 3 {
		t.Fatalf("expected 3 cues, got %d", len(vtt.Cues))
	}

	// First cue
	cue := vtt.Cues[0]
	if cue.Start != 1*time.Second {
		t.Errorf("cue[0].Start = %v, want 1s", cue.Start)
	}
	if cue.End != 4*time.Second {
		t.Errorf("cue[0].End = %v, want 4s", cue.End)
	}
	if cue.Text != "Hello, world!" {
		t.Errorf("cue[0].Text = %q", cue.Text)
	}

	// Third cue (multiline)
	cue3 := vtt.Cues[2]
	if cue3.Text != "Third line with\nmultiple lines." {
		t.Errorf("cue[2].Text = %q", cue3.Text)
	}
}

func TestParseWebVTTCueID(t *testing.T) {
	body := `WEBVTT

cue-1
00:00:01.000 --> 00:00:04.000
First cue

cue-2
00:00:05.000 --> 00:00:08.000
Second cue
`

	vtt, err := ParseWebVTT(body)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}

	if len(vtt.Cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(vtt.Cues))
	}

	if vtt.Cues[0].ID != "cue-1" {
		t.Errorf("cue[0].ID = %q, want cue-1", vtt.Cues[0].ID)
	}
	if vtt.Cues[1].ID != "cue-2" {
		t.Errorf("cue[1].ID = %q, want cue-2", vtt.Cues[1].ID)
	}
}

func TestParseWebVTTMissingHeader(t *testing.T) {
	body := `NOT A VALID WEBVTT

00:00:01.000 --> 00:00:04.000
Hello
`
	_, err := ParseWebVTT(body)
	if err == nil {
		t.Error("expected error for missing WEBVTT header")
	}
}

func TestParseVTTTime(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"00:00:01.000", 1 * time.Second},
		{"01:30:00.000", 90 * time.Minute},
		{"00:01:30.500", 90*time.Second + 500*time.Millisecond},
		{"05:00.000", 5 * time.Minute},
	}
	for _, tt := range tests {
		got, err := parseVTTTime(tt.input)
		if err != nil {
			t.Errorf("parseVTTTime(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseVTTTime(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestFormatVTTTime(t *testing.T) {
	d := 1*time.Hour + 2*time.Minute + 3*time.Second + 456*time.Millisecond
	got := FormatVTTTime(d)
	want := "01:02:03.456"
	if got != want {
		t.Errorf("FormatVTTTime = %q, want %q", got, want)
	}
}

func TestWebVTTToSRT(t *testing.T) {
	vtt := &WebVTT{
		Cues: []Cue{
			{Start: time.Second, End: 4 * time.Second, Text: "Hello"},
			{Start: 5 * time.Second, End: 8 * time.Second, Text: "World"},
		},
	}

	srt := vtt.ToSRT()
	if len(srt) == 0 {
		t.Error("SRT output is empty")
	}

	// Check it contains expected content
	if !contains(srt, "1\n") {
		t.Error("SRT missing sequence number 1")
	}
	if !contains(srt, "Hello") {
		t.Error("SRT missing 'Hello'")
	}
	if !contains(srt, "World") {
		t.Error("SRT missing 'World'")
	}
}

func TestWebVTTShiftBy(t *testing.T) {
	vtt := &WebVTT{
		Cues: []Cue{
			{Start: 5 * time.Second, End: 10 * time.Second, Text: "Test"},
		},
	}

	vtt.ShiftBy(2 * time.Second)

	if vtt.Cues[0].Start != 7*time.Second {
		t.Errorf("Start = %v, want 7s", vtt.Cues[0].Start)
	}
	if vtt.Cues[0].End != 12*time.Second {
		t.Errorf("End = %v, want 12s", vtt.Cues[0].End)
	}
}

func TestParseTTML(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<tt xmlns="http://www.w3.org/ns/ttml">
  <body>
    <div>
      <p begin="00:00:01.000" end="00:00:04.000">Hello, TTML!</p>
      <p begin="00:00:05.000" end="00:00:08.000">Second subtitle</p>
    </div>
  </body>
</tt>`

	vtt, err := ParseTTML(body)
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}

	if len(vtt.Cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(vtt.Cues))
	}

	if vtt.Cues[0].Text != "Hello, TTML!" {
		t.Errorf("cue[0].Text = %q", vtt.Cues[0].Text)
	}
	if vtt.Cues[0].Start != time.Second {
		t.Errorf("cue[0].Start = %v, want 1s", vtt.Cues[0].Start)
	}
}

func TestParseTTMLWithSpans(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<tt xmlns="http://www.w3.org/ns/ttml">
  <body>
    <div>
      <p begin="00:00:01.000" end="00:00:04.000">
        <span>Line one</span>
        <span>Line two</span>
      </p>
    </div>
  </body>
</tt>`

	vtt, err := ParseTTML(body)
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}

	if len(vtt.Cues) != 1 {
		t.Fatalf("expected 1 cue, got %d", len(vtt.Cues))
	}

	if vtt.Cues[0].Text != "Line one\nLine two" {
		t.Errorf("cue[0].Text = %q", vtt.Cues[0].Text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
