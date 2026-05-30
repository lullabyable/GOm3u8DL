package hls

// HLS M3U8 tags defined in RFC 8216.
const (
	// Basic tags
	TagM3U         = "#EXTM3U"
	TagVersion     = "#EXT-X-VERSION"
	TagTargetDuration = "#EXT-X-TARGETDURATION"
	TagMediaSequence  = "#EXT-X-MEDIA-SEQUENCE"
	TagDiscontinuitySequence = "#EXT-X-DISCONTINUITY-SEQUENCE"
	TagEndList     = "#EXT-X-ENDLIST"
	TagPlaylistType = "#EXT-X-PLAYLIST-TYPE"
	TagIFramesOnly = "#EXT-X-I-FRAMES-ONLY"

	// Media segment tags
	TagInf         = "#EXTINF"
	TagByteRange   = "#EXT-X-BYTERANGE"
	TagDiscontinuity = "#EXT-X-DISCONTINUITY"
	TagKey         = "#EXT-X-KEY"
	TagMap         = "#EXT-X-MAP"
	TagProgramDateTime = "#EXT-X-PROGRAM-DATE-TIME"
	TagDateRange   = "#EXT-X-DATERANGE"

	// Media playlist tags (LL-HLS)
	TagPart        = "#EXT-X-PART"
	TagPartInf     = "#EXT-X-PART-INF"
	TagPreloadHint = "#EXT-X-PRELOAD-HINT"
	TagRenditionReport = "#EXT-X-RENDITION-REPORT"
	TagServerControl = "#EXT-X-SERVER-CONTROL"

	// Master playlist tags
	TagStreamInf   = "#EXT-X-STREAM-INF"
	TagMedia       = "#EXT-X-MEDIA"
	TagFrameRate   = "#EXT-X-FRAME-RATE"
	TagIFrameStreamInf = "#EXT-X-I-FRAME-STREAM-INF"
	TagSessionData = "#EXT-X-SESSION-DATA"
	TagSessionKey  = "#EXT-X-SESSION-KEY"

	// Encryption methods
	MethodNone     = "NONE"
	MethodAES128   = "AES-128"
	MethodAES128ECB = "AES-128-ECB"
	MethodSampleAES = "SAMPLE-AES"
	MethodSampleAESCTR = "SAMPLE-AES-CTR"
)
