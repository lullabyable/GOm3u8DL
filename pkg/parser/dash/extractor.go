package dash

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// MPD represents a DASH Media Presentation Description.
type MPD struct {
	XMLName                   xml.Name    `xml:"MPD"`
	XMLNs                     string      `xml:"xmlns,attr"`
	Type                      string      `xml:"type,attr"`
	MediaPresentationDuration string      `xml:"mediaPresentationDuration,attr"`
	MinBufferTime             string      `xml:"minBufferTime,attr"`
	AvailabilityStartTime     string      `xml:"availabilityStartTime,attr"`
	Profiles                  string      `xml:"profiles,attr"`
	Periods                   []Period    `xml:"Period"`
}

// Period represents a DASH Period.
type Period struct {
	ID              string          `xml:"id,attr"`
	Start           string          `xml:"start,attr"`
	Duration        string          `xml:"duration,attr"`
	AdaptationSets  []AdaptationSet `xml:"AdaptationSet"`
}

// AdaptationSet represents a DASH AdaptationSet.
type AdaptationSet struct {
	ID                  string     `xml:"id,attr"`
	ContentType         string     `xml:"contentType,attr"`
	MimeType            string     `xml:"mimeType,attr"`
	Lang                string     `xml:"lang,attr"`
	SegmentAlignment    string     `xml:"segmentAlignment,attr"`
	StartWithSAP        string     `xml:"startWithSAP,attr"`
	Representations     []Representation `xml:"Representation"`
	SegmentTemplate     *SegmentTemplate `xml:"SegmentTemplate"`
	SegmentList         *SegmentList     `xml:"SegmentList"`
	SegmentBase         *SegmentBase     `xml:"SegmentBase"`
	ContentProtection   []ContentProtection `xml:"ContentProtection"`
}

// Representation represents a DASH Representation.
type Representation struct {
	ID              string           `xml:"id,attr"`
	Bandwidth       int              `xml:"bandwidth,attr"`
	Codecs          string           `xml:"codecs,attr"`
	Width           int              `xml:"width,attr"`
	Height          int              `xml:"height,attr"`
	FrameRate       string           `xml:"frameRate,attr"`
	AudioSamplingRate string         `xml:"audioSamplingRate,attr"`
	SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate"`
	SegmentList     *SegmentList     `xml:"SegmentList"`
	SegmentBase     *SegmentBase     `xml:"SegmentBase"`
	BaseURLs        []BaseURL        `xml:"BaseURL"`
}

// BaseURL represents a BaseURL element.
type BaseURL struct {
	Value string `xml:",chardata"`
}

// SegmentTemplate represents a DASH SegmentTemplate.
type SegmentTemplate struct {
	Timescale          int    `xml:"timescale,attr"`
	Initialization     string `xml:"initialization,attr"`
	Media              string `xml:"media,attr"`
	StartNumber        int    `xml:"startNumber,attr"`
	Duration           int    `xml:"duration,attr"`
	SegmentTimeline    *SegmentTimeline `xml:"SegmentTimeline"`
}

// SegmentTimeline represents a DASH SegmentTimeline.
type SegmentTimeline struct {
	Segments []TimelineSegment `xml:"S"`
}

// TimelineSegment represents an S element in SegmentTimeline.
type TimelineSegment struct {
	T uint64 `xml:"t,attr"`
	D uint64 `xml:"d,attr"`
	R int    `xml:"r,attr"`
}

// SegmentList represents a DASH SegmentList.
type SegmentList struct {
	Timescale      int              `xml:"timescale,attr"`
	Duration       int              `xml:"duration,attr"`
	StartNumber    int              `xml:"startNumber,attr"`
	Initialization *Initialization      `xml:"Initialization"`
	SegmentURLs    []SegmentURL     `xml:"SegmentURL"`
}

// SegmentURL represents a SegmentURL element.
type SegmentURL struct {
	Media       string `xml:"media,attr"`
	MediaRange  string `xml:"mediaRange,attr"`
	Index       string `xml:"index,attr"`
	IndexRange  string `xml:"indexRange,attr"`
	SourceURL   string `xml:"sourceURL,attr"`
}

// Initialization represents an Initialization element (used in SegmentList/SegmentBase).
type Initialization struct {
	SourceURL  string `xml:"sourceURL,attr"`
	Range      string `xml:"range,attr"`
}

// SegmentBase represents a DASH SegmentBase.
type SegmentBase struct {
	Timescale   int            `xml:"timescale,attr"`
	IndexRange  string         `xml:"indexRange,attr"`
	Initialization *Initialization `xml:"Initialization"`
}

// ContentProtection represents a DRM content protection element.
type ContentProtection struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
	KID         string `xml:"default_KID,attr"`
}

// Extractor parses DASH MPD manifests.
type Extractor struct {
	BaseURL string
}

// NewExtractor creates a DASH extractor for the given URL.
func NewExtractor(rawURL string) *Extractor {
	return &Extractor{BaseURL: rawURL}
}

// Parse parses an MPD XML body and returns available streams.
func (e *Extractor) Parse(body string) ([]model.StreamInfo, error) {
	var mpd MPD
	if err := xml.Unmarshal([]byte(body), &mpd); err != nil {
		return nil, fmt.Errorf("parse MPD: %w", err)
	}

	var streams []model.StreamInfo

	for _, period := range mpd.Periods {
		for _, as := range period.AdaptationSets {
			for _, rep := range as.Representations {
				s := e.buildStreamInfo(mpd, period, as, rep)
				streams = append(streams, s)
			}
		}
	}

	return streams, nil
}

func (e *Extractor) buildStreamInfo(mpd MPD, period Period, as AdaptationSet, rep Representation) model.StreamInfo {
	s := model.StreamInfo{
		Bandwidth: rep.Bandwidth,
		Codecs:    rep.Codecs,
	}

	// Determine media type
	switch {
	case as.ContentType == "video" || strings.HasPrefix(as.MimeType, "video/"):
		s.MediaType = model.MediaTypeVideo
	case as.ContentType == "audio" || strings.HasPrefix(as.MimeType, "audio/"):
		s.MediaType = model.MediaTypeAudio
	case as.ContentType == "text" || strings.HasPrefix(as.MimeType, "text/"):
		s.MediaType = model.MediaTypeSubtitles
	}

	// Resolution
	if rep.Width > 0 && rep.Height > 0 {
		s.Resolution = fmt.Sprintf("%dx%d", rep.Width, rep.Height)
	} else if s.MediaType == model.MediaTypeVideo {
		s.Resolution = "unknown"
	}

	// Frame rate
	if rep.FrameRate != "" {
		if fr, err := parseFrameRate(rep.FrameRate); err == nil {
			s.FrameRate = fr
		}
	}

	// Language
	s.Language = as.Lang

	// Name
	s.Name = rep.ID
	if s.Name == "" {
		s.Name = as.ID
	}

	// Build playlist from segment info
	s.Playlist = e.buildPlaylist(mpd, period, as, rep)

	// URL (base URL for the representation)
	if len(rep.BaseURLs) > 0 {
		s.URL = resolveURL(e.BaseURL, rep.BaseURLs[0].Value)
	} else {
		s.URL = e.BaseURL
	}

	return s
}

func (e *Extractor) buildPlaylist(mpd MPD, period Period, as AdaptationSet, rep Representation) *model.Playlist {
	pl := &model.Playlist{}

	// Determine duration
	totalDur := parseDuration(mpd.MediaPresentationDuration)
	if totalDur == 0 {
		totalDur = parseDuration(period.Duration)
	}
	pl.TotalDuration = totalDur

	// Get segment template (representation takes priority over adaptation set)
	st := rep.SegmentTemplate
	if st == nil {
		st = as.SegmentTemplate
	}

	if st != nil {
		return e.buildPlaylistFromTemplate(pl, st, totalDur)
	}

	// Segment list
	sl := as.SegmentList
	if sl == nil {
		sl = rep.SegmentList
	}
	if sl != nil {
		return e.buildPlaylistFromList(pl, sl)
	}

	// Segment base - single segment
	sb := as.SegmentBase
	if sb == nil {
		sb = rep.SegmentBase
	}
	if sb != nil {
		// Single segment with byte range
		return pl
	}

	// Single URL (no segmentation)
	return pl
}

func (e *Extractor) buildPlaylistFromTemplate(pl *model.Playlist, st *SegmentTemplate, totalDur float64) *model.Playlist {
	timescale := st.Timescale
	if timescale == 0 {
		timescale = 1
	}

	startNumber := st.StartNumber
	if startNumber == 0 {
		startNumber = 1
	}

	var segments []model.MediaSegment

	if st.SegmentTimeline != nil {
		// Timeline-based segmentation
		segIndex := int64(startNumber)
		time := uint64(0)

		for _, s := range st.SegmentTimeline.Segments {
			repeat := s.R
			if repeat < 0 {
				repeat = 0
			}

			if s.T > 0 {
				time = s.T
			}

			for i := 0; i <= repeat; i++ {
				segURL := replaceTemplate(st.Media, segIndex, time, timescale)
				seg := model.MediaSegment{
					Index:    segIndex,
					Duration: float64(s.D) / float64(timescale),
					URL:      resolveURL(e.BaseURL, segURL),
				}
				segments = append(segments, seg)
				time += s.D
				segIndex++
			}
		}
	} else if st.Duration > 0 {
		// Duration-based segmentation
		segDuration := float64(st.Duration) / float64(timescale)
		numSegments := int(totalDur / segDuration)
		if numSegments == 0 {
			numSegments = 1
		}

		for i := 0; i < numSegments; i++ {
			segIndex := int64(startNumber + i)
			time := uint64(i * st.Duration)
			segURL := replaceTemplate(st.Media, segIndex, time, timescale)
			seg := model.MediaSegment{
				Index:    segIndex,
				Duration: segDuration,
				URL:      resolveURL(e.BaseURL, segURL),
			}
			segments = append(segments, seg)
		}
	}

	// Init segment
	if st.Initialization != "" {
		initURL := replaceTemplate(st.Initialization, 0, 0, timescale)
		pl.MediaInit = &model.MediaSegment{
			Index: -1,
			URL:   resolveURL(e.BaseURL, initURL),
		}
	}

	pl.MediaParts = []model.MediaPart{{MediaSegments: segments}}
	return pl
}

func (e *Extractor) buildPlaylistFromList(pl *model.Playlist, sl *SegmentList) *model.Playlist {
	timescale := sl.Timescale
	if timescale == 0 {
		timescale = 1
	}

	startNumber := sl.StartNumber
	if startNumber == 0 {
		startNumber = 1
	}

	// Init segment
	if sl.Initialization != nil && sl.Initialization.SourceURL != "" {
		pl.MediaInit = &model.MediaSegment{
			Index: -1,
			URL:   resolveURL(e.BaseURL, sl.Initialization.SourceURL),
		}
	}

	var segments []model.MediaSegment
	for i, su := range sl.SegmentURLs {
		segIndex := int64(startNumber + i)
		segDur := float64(sl.Duration) / float64(timescale)
		seg := model.MediaSegment{
			Index:    segIndex,
			Duration: segDur,
			URL:      resolveURL(e.BaseURL, su.Media),
		}
		segments = append(segments, seg)
	}

	pl.MediaParts = []model.MediaPart{{MediaSegments: segments}}
	return pl
}

// --- Utility functions ---

func parseDuration(s string) float64 {
	if s == "" {
		return 0
	}
	// ISO 8601 duration: PT1H2M3.4S
	s = strings.TrimPrefix(s, "PT")
	var total float64
	var num strings.Builder

	for _, c := range s {
		switch c {
		case 'H':
			if v, err := strconv.ParseFloat(num.String(), 64); err == nil {
				total += v * 3600
			}
			num.Reset()
		case 'M':
			if v, err := strconv.ParseFloat(num.String(), 64); err == nil {
				total += v * 60
			}
			num.Reset()
		case 'S':
			if v, err := strconv.ParseFloat(num.String(), 64); err == nil {
				total += v
			}
			num.Reset()
		default:
			num.WriteRune(c)
		}
	}
	return total
}

func parseFrameRate(s string) (float64, error) {
	// Can be "30" or "30000/1001"
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 1 {
		return strconv.ParseFloat(parts[0], 64)
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0, fmt.Errorf("invalid frame rate: %s", s)
	}
	return num / den, nil
}

// replaceTemplate replaces DASH template variables.
// $Number$, $Time$, $RepresentationID$
func replaceTemplate(template string, number int64, time uint64, timescale int) string {
	s := template
	s = strings.ReplaceAll(s, "$RepresentationID$", "") // handled by caller
	s = strings.ReplaceAll(s, "$Number$", strconv.FormatInt(number, 10))
	s = strings.ReplaceAll(s, "$Time$", strconv.FormatUint(time, 10))
	s = strings.ReplaceAll(s, "$Timescale$", strconv.Itoa(timescale))

	// Handle format width: $Number%05d$
	// Simplified: just do basic replacement
	return s
}

func resolveURL(base, ref string) string {
	if ref == "" {
		return base
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "/") {
		// Absolute path
		idx := strings.Index(base, "://")
		if idx < 0 {
			return ref
		}
		hostEnd := strings.Index(base[idx+3:], "/")
		if hostEnd < 0 {
			return base + ref
		}
		return base[:idx+3+hostEnd] + ref
	}
	// Relative path
	idx := strings.LastIndex(base, "/")
	if idx < 0 {
		return ref
	}
	return base[:idx+1] + ref
}

// DurationToTime converts a float duration (seconds) to time.Duration.
func DurationToTime(d float64) time.Duration {
	return time.Duration(d * float64(time.Second))
}
