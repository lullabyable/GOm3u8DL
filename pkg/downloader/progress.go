package downloader

import (
	"sync"
	"sync/atomic"
	"time"
)

// ProgressTracker tracks download progress across multiple segments.
type ProgressTracker struct {
	totalBytes    int64
	downloaded    int64
	segmentsTotal int32
	segmentsDone  int32
	startTime     time.Time
	mu            sync.Mutex
	speedSamples  []speedSample
	// For estimating total size from completed segments
	estimatedTotal int64
	sampleBytes    int64 // cumulative bytes from completed segments (for estimation)
}

type speedSample struct {
	bytes int64
	at    time.Time
}

// NewProgressTracker creates a new progress tracker.
func NewProgressTracker(totalSegments int) *ProgressTracker {
	return &ProgressTracker{
		segmentsTotal: int32(totalSegments),
		startTime:     time.Now(),
		speedSamples:  make([]speedSample, 0, 100),
	}
}

// SetTotalBytes sets the expected total bytes (if known).
func (pt *ProgressTracker) SetTotalBytes(total int64) {
	atomic.StoreInt64(&pt.totalBytes, total)
}

// AddBytes records downloaded bytes.
func (pt *ProgressTracker) AddBytes(n int64) {
	atomic.AddInt64(&pt.downloaded, n)
	pt.mu.Lock()
	pt.speedSamples = append(pt.speedSamples, speedSample{bytes: n, at: time.Now()})
	pt.mu.Unlock()
}

// SegmentDone records a completed segment.
func (pt *ProgressTracker) SegmentDone() {
	atomic.AddInt32(&pt.segmentsDone, 1)
}

// Progress returns the current progress snapshot.
func (pt *ProgressTracker) Progress() Progress {
	downloaded := atomic.LoadInt64(&pt.downloaded)
	total := atomic.LoadInt64(&pt.totalBytes)
	done := atomic.LoadInt32(&pt.segmentsDone)
	totalSeg := atomic.LoadInt32(&pt.segmentsTotal)
	elapsed := time.Since(pt.startTime).Seconds()

	p := Progress{
		Downloaded:   downloaded,
		Total:        total,
		SegmentsDone: int(done),
		Segments:     int(totalSeg),
		Elapsed:      elapsed,
	}

	if elapsed > 0 {
		p.AvgSpeed = int64(float64(downloaded) / elapsed)
	}

	// Calculate current speed from recent samples (last 2 seconds)
	pt.mu.Lock()
	now := time.Now()
	var recentBytes int64
	var oldest time.Time
	for i := len(pt.speedSamples) - 1; i >= 0; i-- {
		s := pt.speedSamples[i]
		if now.Sub(s.at) > 2*time.Second {
			break
		}
		recentBytes += s.bytes
		oldest = s.at
	}
	pt.mu.Unlock()

	if recentBytes > 0 && !oldest.IsZero() {
		dur := now.Sub(oldest).Seconds()
		if dur > 0 {
			p.Speed = int64(float64(recentBytes) / dur)
		}
	}

	// If total not set, estimate from completed segments
	if total <= 0 && done > 0 && downloaded > 0 {
		avgSegSize := float64(downloaded) / float64(done)
		total = int64(avgSegSize * float64(totalSeg))
		pt.estimatedTotal = total
	} else if total <= 0 && pt.estimatedTotal > 0 {
		total = pt.estimatedTotal
	}

	if total > 0 {
		p.Total = total
		p.Percent = float64(downloaded) / float64(total) * 100
		if p.Percent > 100 {
			p.Percent = 100
		}
		if p.Speed > 0 {
			remaining := float64(total-downloaded) / float64(p.Speed)
			p.ETA = remaining
		}
	} else if totalSeg > 0 {
		p.Percent = float64(done) / float64(totalSeg) * 100
	}

	return p
}

// Progress is a point-in-time snapshot of download progress.
type Progress struct {
	Total        int64
	Downloaded   int64
	Speed        int64   // current speed bytes/sec
	AvgSpeed     int64   // average speed bytes/sec
	Segments     int
	SegmentsDone int
	Percent      float64
	ETA          float64 // seconds remaining
	Elapsed      float64 // seconds elapsed
}
