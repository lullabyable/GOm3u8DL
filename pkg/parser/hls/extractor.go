package hls

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// Extractor parses HLS M3U8 playlists (master and media).
type Extractor struct {
	BaseURL string
}

// NewExtractor creates an HLS extractor for the given URL.
func NewExtractor(rawURL string) *Extractor {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &Extractor{BaseURL: rawURL}
	}
	u.Path = dirPath(u.Path)
	u.RawQuery = ""
	u.Fragment = ""
	return &Extractor{BaseURL: u.String()}
}

// Parse parses an M3U8 playlist body and returns streams (for master)
// or a single stream with playlist (for media).
func (e *Extractor) Parse(body string) ([]model.StreamInfo, *model.Playlist, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))

	if !scanner.Scan() {
		return nil, nil, fmt.Errorf("empty playlist")
	}
	firstLine := strings.TrimSpace(scanner.Text())
	if firstLine != TagM3U {
		return nil, nil, fmt.Errorf("not a valid M3U8 playlist: missing #EXTM3U")
	}

	// Collect all lines
	isMaster := false
	lines := []string{firstLine}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
		// Detect master playlist by #EXT-X-STREAM-INF only
		// (not #EXT-X-MEDIA which conflicts with #EXT-X-MEDIA-SEQUENCE)
		if strings.HasPrefix(line, TagStreamInf) {
			isMaster = true
		}
	}

	if isMaster {
		streams, err := e.parseMasterPlaylist(lines)
		return streams, nil, err
	}

	playlist, err := e.parseMediaPlaylist(lines)
	return nil, playlist, err
}

// parseMasterPlaylist parses a master playlist and returns available streams.
func (e *Extractor) parseMasterPlaylist(lines []string) ([]model.StreamInfo, error) {
	var streams []model.StreamInfo

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.HasPrefix(line, TagStreamInf) {
			attrs, err := parseAttributes(line)
			if err != nil {
				return nil, fmt.Errorf("parse STREAM-INF at line %d: %w", i, err)
			}

			// Next non-tag line should be the URL
			var streamURL string
			for i+1 < len(lines) {
				i++
				if !strings.HasPrefix(lines[i], "#") {
					streamURL = resolveURL(e.BaseURL, lines[i])
					break
				}
			}
			if streamURL == "" {
				return nil, fmt.Errorf("STREAM-INF without URL at line %d", i)
			}

			s := model.StreamInfo{
				MediaType: model.MediaTypeVideo,
				URL:       streamURL,
			}

			if bw, ok := attrs["BANDWIDTH"]; ok {
				s.Bandwidth, _ = strconv.Atoi(bw)
			}
			if res, ok := attrs["RESOLUTION"]; ok {
				s.Resolution = res
			}
			if codecs, ok := attrs["CODECS"]; ok {
				s.Codecs = codecs
			}
			if fr, ok := attrs["FRAME-RATE"]; ok {
				s.FrameRate, _ = strconv.ParseFloat(fr, 64)
			}
			if name, ok := attrs["NAME"]; ok {
				s.Name = name
			}

			streams = append(streams, s)
		}

		if strings.HasPrefix(line, TagMedia) && !strings.HasPrefix(line, TagMediaSequence) {
			attrs, err := parseAttributes(line)
			if err != nil {
				continue
			}

			mediaType := model.MediaTypeVideo
			switch strings.ToUpper(attrs["TYPE"]) {
			case "AUDIO":
				mediaType = model.MediaTypeAudio
			case "SUBTITLES":
				mediaType = model.MediaTypeSubtitles
			}

			s := model.StreamInfo{
				MediaType: mediaType,
				GroupID:   attrs["GROUP-ID"],
				Language:  attrs["LANGUAGE"],
				Name:      attrs["NAME"],
			}

			if uri, ok := attrs["URI"]; ok {
				s.URL = resolveURL(e.BaseURL, uri)
			}

			if s.URL != "" {
				streams = append(streams, s)
			}
		}
	}

	return streams, nil
}

// parseMediaPlaylist parses a media playlist and returns a Playlist with segments.
func (e *Extractor) parseMediaPlaylist(lines []string) (*model.Playlist, error) {
	pl := &model.Playlist{}
	var segments []model.MediaSegment
	var currentSeg model.MediaSegment
	var currentEncrypt model.EncryptInfo
	var mediaIndex int64

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, TagTargetDuration):
			val := strings.TrimPrefix(line, TagTargetDuration+":")
			if td, err := strconv.ParseFloat(val, 64); err == nil {
				pl.TargetDuration = &td
			}

		case strings.HasPrefix(line, TagMediaSequence):
			val := strings.TrimPrefix(line, TagMediaSequence+":")
			seq, _ := strconv.ParseInt(val, 10, 64)
			mediaIndex = seq

		case strings.HasPrefix(line, TagEndList):
			pl.IsLive = false

		case strings.HasPrefix(line, TagKey):
			key, err := parseKeyTag(e.BaseURL, line)
			if err == nil {
				currentEncrypt = key
			}

		case strings.HasPrefix(line, TagMap):
			attrs, err := parseAttributes(line)
			if err == nil {
				if uri, ok := attrs["URI"]; ok {
					initSeg := &model.MediaSegment{
						URL:      resolveURL(e.BaseURL, uri),
						Index:    -1,
						Duration: 0,
					}
					if br, ok := attrs["BYTERANGE"]; ok {
						offset, length := parseByteRange(br)
						initSeg.StartRange = &offset
						initSeg.ExpectLength = &length
					}
					pl.MediaInit = initSeg
				}
			}

		case strings.HasPrefix(line, TagInf):
			val := strings.TrimPrefix(line, TagInf+":")
			parts := strings.SplitN(val, ",", 2)
			if dur, err := strconv.ParseFloat(parts[0], 64); err == nil {
				currentSeg.Duration = dur
				currentSeg.Index = mediaIndex
				currentSeg.EncryptInfo = currentEncrypt
				// RFC 8216 Section 4.3.2.4: If the IV is not present,
				// use the media sequence number as the IV (16-byte big-endian).
				if currentSeg.EncryptInfo.Method == model.EncryptMethodAES128 &&
					len(currentSeg.EncryptInfo.IV) == 0 {
					iv := make([]byte, 16)
					binary.BigEndian.PutUint64(iv[8:], uint64(mediaIndex))
					currentSeg.EncryptInfo.IV = iv
				}
				mediaIndex++
			}

		case strings.HasPrefix(line, TagByteRange):
			val := strings.TrimPrefix(line, TagByteRange+":")
			offset, length := parseByteRange(val)
			currentSeg.StartRange = &offset
			currentSeg.ExpectLength = &length

		case strings.HasPrefix(line, TagDiscontinuity):
			// TODO: handle discontinuity

		case !strings.HasPrefix(line, "#"):
			// This is a segment URL
			currentSeg.URL = resolveURL(e.BaseURL, line)
			segments = append(segments, currentSeg)
			currentSeg = model.MediaSegment{}
		}
	}

	// Calculate total duration
	var totalDur float64
	for _, seg := range segments {
		totalDur += seg.Duration
	}
	pl.TotalDuration = totalDur
	pl.MediaParts = []model.MediaPart{{MediaSegments: segments}}

	// Live playlist if no #EXT-X-ENDLIST was found
	// pl.IsLive defaults to false; if we never saw #EXT-X-ENDLIST and have segments, mark live
	if len(segments) > 0 {
		// Check if #EXT-X-ENDLIST was present by scanning lines again
		hasEndList := false
		for _, l := range lines {
			if l == TagEndList {
				hasEndList = true
				break
			}
		}
		if !hasEndList {
			pl.IsLive = true
			if pl.TargetDuration != nil {
				pl.RefreshInterval = *pl.TargetDuration * 1000 // ms
			}
		}
	}

	return pl, nil
}

// --- Utility functions ---

// parseAttributes parses KEY=VALUE pairs from an HLS tag line.
// Handles quoted strings and unquoted values.
func parseAttributes(line string) (map[string]string, error) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return nil, fmt.Errorf("no colon in attribute line: %s", line)
	}
	line = line[idx+1:]

	attrs := make(map[string]string)
	for line != "" {
		line = strings.TrimLeft(line, " ,")
		if line == "" {
			break
		}

		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			break
		}
		key := strings.TrimSpace(line[:eqIdx])
		line = line[eqIdx+1:]

		var value string
		if strings.HasPrefix(line, "\"") {
			endQuote := strings.Index(line[1:], "\"")
			if endQuote < 0 {
				value = line[1:]
				line = ""
			} else {
				value = line[1 : endQuote+1]
				line = line[endQuote+2:]
			}
		} else {
			commaIdx := strings.Index(line, ",")
			if commaIdx < 0 {
				value = line
				line = ""
			} else {
				value = line[:commaIdx]
				line = line[commaIdx:]
			}
		}

		attrs[key] = value
	}

	return attrs, nil
}

// resolveURL resolves a potentially relative URL against a base URL.
func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	bu, err := url.Parse(base)
	if err != nil {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return bu.ResolveReference(ru).String()
}

// dirPath returns the directory portion of a URL path.
func dirPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i+1]
		}
	}
	return "/"
}

// parseByteRange parses "length@offset" byte range specification.
func parseByteRange(s string) (offset, length int64) {
	parts := strings.SplitN(s, "@", 2)
	length, _ = strconv.ParseInt(parts[0], 10, 64)
	if len(parts) > 1 {
		offset, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	return
}
