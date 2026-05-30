package subtitle

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// TTML represents a parsed TTML subtitle document.
type TTML struct {
	Body TTMLBody `xml:"body"`
}

// TTMLBody represents the body element of a TTML document.
type TTMLBody struct {
	Divs []TTMLDiv `xml:"div"`
}

// TTMLDiv represents a div element in TTML.
type TTMLDiv struct {
	Paragraphs []TTMLParagraph `xml:"p"`
}

// TTMLParagraph represents a p element in TTML.
type TTMLParagraph struct {
	Begin string `xml:"begin,attr"`
	End   string `xml:"end,attr"`
	Text  string `xml:",chardata"`
	Span  []struct {
		Begin string `xml:"begin,attr"`
		End   string `xml:"end,attr"`
		Text  string `xml:",chardata"`
	} `xml:"span"`
}

// ParseTTML parses a TTML XML string and returns WebVTT cues.
func ParseTTML(body string) (*WebVTT, error) {
	var ttml TTML
	if err := xml.Unmarshal([]byte(body), &ttml); err != nil {
		return nil, fmt.Errorf("parse TTML: %w", err)
	}

	vtt := &WebVTT{}

	for _, div := range ttml.Body.Divs {
		for _, p := range div.Paragraphs {
			start, err := parseTTMLTime(p.Begin)
			if err != nil {
				continue
			}
			end, err := parseTTMLTime(p.End)
			if err != nil {
				continue
			}

			text := strings.TrimSpace(p.Text)
			if text == "" && len(p.Span) > 0 {
				var parts []string
				for _, span := range p.Span {
					parts = append(parts, strings.TrimSpace(span.Text))
				}
				text = strings.Join(parts, "\n")
			}

			if text != "" {
				vtt.Cues = append(vtt.Cues, Cue{
					Start: start,
					End:   end,
					Text:  text,
				})
			}
		}
	}

	return vtt, nil
}

// parseTTMLTime parses a TTML time expression.
// Supports: "HH:MM:SS", "HH:MM:SS.mmm", "SSS.mms", "NNNf" (frames), "NNNt" (ticks).
func parseTTMLTime(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}

	// Check for frames (e.g., "30f")
	if strings.HasSuffix(s, "f") {
		var frames int
		fmt.Sscanf(s[:len(s)-1], "%d", &frames)
		// Assume 30fps
		return time.Duration(frames) * time.Second / 30, nil
	}

	// Check for ticks (e.g., "1000t")
	if strings.HasSuffix(s, "t") {
		var ticks int
		fmt.Sscanf(s[:len(s)-1], "%d", &ticks)
		return time.Duration(ticks) * time.Microsecond, nil
	}

	// Check for seconds (e.g., "123.456s")
	if strings.HasSuffix(s, "s") {
		var sec float64
		fmt.Sscanf(s[:len(s)-1], "%f", &sec)
		return time.Duration(sec * float64(time.Second)), nil
	}

	// HH:MM:SS or HH:MM:SS.mmm
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		var hours, minutes int
		var seconds float64
		fmt.Sscanf(parts[0], "%d", &hours)
		fmt.Sscanf(parts[1], "%d", &minutes)
		fmt.Sscanf(parts[2], "%f", &seconds)
		return time.Duration(hours)*time.Hour +
			time.Duration(minutes)*time.Minute +
			time.Duration(seconds*float64(time.Second)), nil
	}

	return 0, fmt.Errorf("unsupported TTML time format: %s", s)
}
