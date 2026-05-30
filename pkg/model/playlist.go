package model

// Playlist represents a parsed media playlist containing segments.
type Playlist struct {
	URL             string
	IsLive          bool
	RefreshInterval float64 // ms, for live playlists
	TotalDuration   float64 // seconds
	TargetDuration  *float64
	MediaInit       *MediaSegment
	MediaParts      []MediaPart
}
