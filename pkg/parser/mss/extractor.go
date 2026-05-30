package mss

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// SmoothStreamingMedia is the root element of an ISM manifest.
type SmoothStreamingMedia struct {
	XMLName        xml.Name      `xml:"SmoothStreamingMedia"`
	MajorVersion   int           `xml:"MajorVersion,attr"`
	MinorVersion   int           `xml:"MinorVersion,attr"`
	Duration       int64         `xml:"Duration,attr"`
	IsLive         bool          `xml:"IsLive,attr"`
	StreamIndexes  []StreamIndex `xml:"StreamIndex"`
}

// StreamIndex represents a single stream type (video, audio, text).
type StreamIndex struct {
	Type          string        `xml:"Type,attr"` // video, audio, text
	Name          string        `xml:"Name,attr"`
	Language      string        `xml:"Language,attr"`
	Chunks        int           `xml:"Chunks,attr"`
	QualityLevels []QualityLevel `xml:"QualityLevel"`
	Chunks2       []Chunk       `xml:"c"`
}

// QualityLevel represents a single quality variant within a stream.
type QualityLevel struct {
	Index             int    `xml:"Index,attr"`
	Bitrate           int    `xml:"Bitrate,attr"`
	FourCC            string `xml:"FourCC,attr"`
	MaxWidth          int    `xml:"MaxWidth,attr"`
	MaxHeight         int    `xml:"MaxHeight,attr"`
	SamplingRate      int    `xml:"SamplingRate,attr"`
	Channels          int    `xml:"Channels,attr"`
	CodecPrivateData  string `xml:"CodecPrivateData,attr"`
}

// Chunk represents a single media chunk with duration and optional timestamp.
type Chunk struct {
	Duration  int64 `xml:"d,attr"` // 100ns units
	Timestamp int64 `xml:"t,attr"` // 100ns units, cumulative
}

// Extractor parses Microsoft Smooth Streaming (ISM) manifests.
type Extractor struct {
	baseURL string
}

// NewExtractor creates an MSS extractor for the given base URL.
func NewExtractor(baseURL string) *Extractor {
	return &Extractor{baseURL: baseURL}
}

// Parse parses an ISM XML body and returns available streams.
func (e *Extractor) Parse(body string) ([]model.StreamInfo, error) {
	var ssm SmoothStreamingMedia
	if err := xml.Unmarshal([]byte(body), &ssm); err != nil {
		return nil, fmt.Errorf("parse ISM: %w", err)
	}

	var streams []model.StreamInfo

	for _, si := range ssm.StreamIndexes {
		// Skip text streams for now
		if si.Type == "text" {
			continue
		}

		for _, ql := range si.QualityLevels {
			s := e.buildStreamInfo(ssm, si, ql)
			streams = append(streams, s)
		}
	}

	return streams, nil
}

func (e *Extractor) buildStreamInfo(ssm SmoothStreamingMedia, si StreamIndex, ql QualityLevel) model.StreamInfo {
	s := model.StreamInfo{
		Bandwidth: ql.Bitrate,
		Language:  si.Language,
	}

	// Determine media type
	switch si.Type {
	case "video":
		s.MediaType = model.MediaTypeVideo
		if ql.MaxWidth > 0 && ql.MaxHeight > 0 {
			s.Resolution = fmt.Sprintf("%dx%d", ql.MaxWidth, ql.MaxHeight)
		}
	case "audio":
		s.MediaType = model.MediaTypeAudio
		if ql.Channels > 0 {
			s.Channels = fmt.Sprintf("%d", ql.Channels)
		}
	}

	// Codec from FourCC
	s.Codecs = fourCCToCodec(ql.FourCC)

	// Name
	s.Name = si.Name
	if ql.MaxHeight > 0 {
		s.Name = fmt.Sprintf("%s_%dp", si.Name, ql.MaxHeight)
	}

	// Build playlist
	s.Playlist = e.buildPlaylist(ssm, si, ql)

	// URL points to the manifest itself
	s.URL = e.baseURL

	return s
}

func (e *Extractor) buildPlaylist(ssm SmoothStreamingMedia, si StreamIndex, ql QualityLevel) *model.Playlist {
	pl := &model.Playlist{
		IsLive: ssm.IsLive,
	}

	// Total duration in seconds (Duration is in 100ns units)
	if ssm.Duration > 0 {
		pl.TotalDuration = float64(ssm.Duration) / 10_000_000
	}

	// Build chunks: either from explicit <c> elements or synthesize from Chunks attribute
	chunks := si.Chunks2
	if len(chunks) == 0 && si.Chunks > 0 && ssm.Duration > 0 {
		// Synthesize uniform chunks from Duration/Chunks
		chunkDur := ssm.Duration / int64(si.Chunks)
		for i := 0; i < si.Chunks; i++ {
			chunks = append(chunks, Chunk{
				Timestamp: int64(i) * chunkDur,
				Duration:  chunkDur,
			})
		}
	}

	// Build segments from chunks
	var segments []model.MediaSegment
	var currentTS int64

	for i, c := range chunks {
		ts := c.Timestamp
		if ts == 0 && i > 0 {
			// If no explicit timestamp, accumulate from previous
			ts = currentTS
		}
		currentTS = ts + c.Duration

		segURL := e.buildSegmentURL(si, ql, ts)
		seg := model.MediaSegment{
			Index:    int64(i),
			Duration: float64(c.Duration) / 10_000_000, // convert 100ns to seconds
			URL:      segURL,
		}
		segments = append(segments, seg)
	}

	pl.MediaParts = []model.MediaPart{{MediaSegments: segments}}
	return pl
}

// buildSegmentURL constructs the fragment URL for an MSS segment.
// Standard pattern: {baseURL}/QualityLevels({bitrate})/Fragments({streamName}={timestamp})
func (e *Extractor) buildSegmentURL(si StreamIndex, ql QualityLevel, timestamp int64) string {
	base := strings.TrimRight(e.baseURL, "/")
	return fmt.Sprintf("%s/QualityLevels(%d)/Fragments(%s=%d)",
		base, ql.Bitrate, si.Name, timestamp)
}

// fourCCToCodec converts an MSS FourCC code to a codec string.
func fourCCToCodec(fourCC string) string {
	switch strings.ToUpper(fourCC) {
	case "H264", "AVC1":
		return "avc1"
	case "H265", "HEVC", "HEV1":
		return "hev1"
	case "AV01":
		return "av01"
	case "VP09", "VP9":
		return "vp09"
	case "AACL", "AAC":
		return "mp4a.40.2"
	case "EC-3", "EC3", "EAC3":
		return "ec-3"
	case "AC-3", "AC3":
		return "ac-3"
	case "OPUS":
		return "opus"
	case "FLAC":
		return "fLaC"
	default:
		return strings.ToLower(fourCC)
	}
}
