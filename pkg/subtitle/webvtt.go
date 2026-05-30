package subtitle

import (
	"fmt"
	"strings"
	"time"
)

// WebVTT represents a parsed WebVTT subtitle file.
type WebVTT struct {
	Cues []Cue
}

// Cue represents a single subtitle cue.
type Cue struct {
	ID       string
	Start    time.Duration
	End      time.Duration
	Text     string
	Settings string // positioning settings
}

// ParseWebVTT parses a WebVTT formatted string.
func ParseWebVTT(body string) (*WebVTT, error) {
	vtt := &WebVTT{}
	lines := strings.Split(body, "\n")

	// Find WEBVTT header
	foundHeader := false
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "WEBVTT") {
			foundHeader = true
			i++
			break
		}
		i++
	}

	if !foundHeader {
		return nil, fmt.Errorf("missing WEBVTT header")
	}

	// Skip metadata and blank lines
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		// Check if this is a cue
		if strings.Contains(line, "-->") {
			// This is a timestamp line (no cue ID)
			cue, consumed, err := parseCue(lines, i, "")
			if err != nil {
				i++
				continue
			}
			vtt.Cues = append(vtt.Cues, cue)
			i += consumed
		} else if i+1 < len(lines) && strings.Contains(strings.TrimSpace(lines[i+1]), "-->") {
			// This is a cue ID, next line is timestamp
			cue, consumed, err := parseCue(lines, i+1, line)
			if err != nil {
				i++
				continue
			}
			vtt.Cues = append(vtt.Cues, cue)
			i += consumed + 1
		} else {
			i++
		}
	}

	return vtt, nil
}

func parseCue(lines []string, tsIndex int, id string) (Cue, int, error) {
	cue := Cue{ID: id}

	// Parse timestamp line
	tsLine := strings.TrimSpace(lines[tsIndex])
	parts := strings.SplitN(tsLine, "-->", 2)
	if len(parts) != 2 {
		return cue, 0, fmt.Errorf("invalid timestamp line: %s", tsLine)
	}

	start, err := parseVTTTime(strings.TrimSpace(parts[0]))
	if err != nil {
		return cue, 0, fmt.Errorf("parse start time: %w", err)
	}
	end, err := parseVTTTime(strings.TrimSpace(parts[1]))
	if err != nil {
		return cue, 0, fmt.Errorf("parse end time: %w", err)
	}

	cue.Start = start
	cue.End = end

	// Settings (after the end time)
	endPart := strings.TrimSpace(parts[1])
	if spaceIdx := strings.Index(endPart, " "); spaceIdx > 0 {
		cue.Settings = strings.TrimSpace(endPart[spaceIdx:])
	}

	// Collect text lines
	consumed := 1 // timestamp line
	i := tsIndex + 1
	var textLines []string
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			consumed++
			break
		}
		textLines = append(textLines, line)
		consumed++
		i++
	}

	cue.Text = strings.Join(textLines, "\n")
	return cue, consumed, nil
}

// parseVTTTime parses a WebVTT timestamp (HH:MM:SS.mmm or MM:SS.mmm).
func parseVTTTime(s string) (time.Duration, error) {
	// Remove any whitespace
	s = strings.TrimSpace(s)

	parts := strings.Split(s, ":")
	var hours, minutes int
	var seconds float64
	var err error

	switch len(parts) {
	case 2:
		// MM:SS.mmm
		_, err = fmt.Sscanf(parts[0], "%d", &minutes)
		if err != nil {
			return 0, fmt.Errorf("parse minutes: %w", err)
		}
		_, err = fmt.Sscanf(parts[1], "%f", &seconds)
		if err != nil {
			return 0, fmt.Errorf("parse seconds: %w", err)
		}
	case 3:
		// HH:MM:SS.mmm
		_, err = fmt.Sscanf(parts[0], "%d", &hours)
		if err != nil {
			return 0, fmt.Errorf("parse hours: %w", err)
		}
		_, err = fmt.Sscanf(parts[1], "%d", &minutes)
		if err != nil {
			return 0, fmt.Errorf("parse minutes: %w", err)
		}
		_, err = fmt.Sscanf(parts[2], "%f", &seconds)
		if err != nil {
			return 0, fmt.Errorf("parse seconds: %w", err)
		}
	default:
		return 0, fmt.Errorf("invalid time format: %s", s)
	}

	d := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds*float64(time.Second))
	return d, nil
}

// String formats a WebVTT duration back to timestamp format.
func FormatVTTTime(d time.Duration) string {
	h := d / time.Hour
	d %= time.Hour
	m := d / time.Minute
	d %= time.Minute
	s := d.Seconds()
	return fmt.Sprintf("%02d:%02d:%06.3f", h, m, s)
}

// ToSRT converts WebVTT to SRT format.
func (vtt *WebVTT) ToSRT() string {
	var sb strings.Builder
	for i, cue := range vtt.Cues {
		sb.WriteString(fmt.Sprintf("%d\n", i+1))
		sb.WriteString(fmt.Sprintf("%s --> %s\n", FormatSRTTime(cue.Start), FormatSRTTime(cue.End)))
		sb.WriteString(cue.Text)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// FormatSRTTime formats a duration in SRT format (HH:MM:SS,mmm).
func FormatSRTTime(d time.Duration) string {
	h := d / time.Hour
	d %= time.Hour
	m := d / time.Minute
	d %= time.Minute
	s := d.Seconds()
	return fmt.Sprintf("%02d:%02d:%06.3f", h, m, s)
}

// ShiftBy shifts all cue times by the given offset.
func (vtt *WebVTT) ShiftBy(offset time.Duration) {
	for i := range vtt.Cues {
		vtt.Cues[i].Start += offset
		vtt.Cues[i].End += offset
	}
}
