package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// ProgressFunc is called periodically with download progress.
type ProgressFunc func(Progress)

// Manager orchestrates downloading all segments of a playlist.
type Manager struct {
	client    *http.Client
	concur    int
	retries   int
	onProgress ProgressFunc
	limiter   *Limiter
}

// NewManager creates a new download manager.
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		client:  &http.Client{Timeout: 30 * time.Second},
		concur:  8,
		retries: 3,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ManagerOption configures the Manager.
type ManagerOption func(*Manager)

func WithConcurrency(n int) ManagerOption {
	return func(m *Manager) { m.concur = n }
}

func WithRetries(n int) ManagerOption {
	return func(m *Manager) { m.retries = n }
}

func WithHTTPClient(c *http.Client) ManagerOption {
	return func(m *Manager) { m.client = c }
}

func WithProgressFunc(fn ProgressFunc) ManagerOption {
	return func(m *Manager) { m.onProgress = fn }
}

// WithLimiter sets a rate limiter for download speed control.
func WithLimiter(l *Limiter) ManagerOption {
	return func(m *Manager) { m.limiter = l }
}

// DownloadSegments downloads all segments from a playlist and returns the file paths in order.
func (m *Manager) DownloadSegments(ctx context.Context, playlist *model.Playlist, tempDir string) ([]string, error) {
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", tempDir, err)
	}

	// Collect all segments from all parts
	var allSegments []model.MediaSegment
	for _, part := range playlist.MediaParts {
		allSegments = append(allSegments, part.MediaSegments...)
	}

	if len(allSegments) == 0 {
		return nil, fmt.Errorf("no segments to download")
	}

	// Download init segment if present
	if playlist.MediaInit != nil {
		sd := NewSegmentDownloader(m.client, m.retries)
		result := sd.Download(ctx, *playlist.MediaInit, tempDir, -1)
		if result.Error != nil {
			return nil, fmt.Errorf("download init segment: %w", result.Error)
		}
	}

	tracker := NewProgressTracker(len(allSegments))

	// Estimate total size via HEAD request on first segment
	if len(allSegments) > 0 {
		go func() {
			firstSeg := allSegments[0]
			headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, firstSeg.URL, nil)
			if err == nil {
				headResp, err := m.client.Do(headReq)
				if err == nil {
					headResp.Body.Close()
					if headResp.ContentLength > 0 {
						estimated := headResp.ContentLength * int64(len(allSegments))
						tracker.SetTotalBytes(estimated)
					}
				}
			}
		}()
	}

	// Progress reporting goroutine
	if m.onProgress != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					m.onProgress(tracker.Progress())
				}
			}
		}()
	}

	// Worker pool
	type indexedResult struct {
		index  int
		result DownloadResult
	}

	jobs := make(chan int, len(allSegments))
	results := make(chan indexedResult, len(allSegments))

	var wg sync.WaitGroup
	for i := 0; i < m.concur; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sd := NewSegmentDownloader(m.client, m.retries)
			for idx := range jobs {
				seg := allSegments[idx]
				// Rate limit: wait for permission before downloading
				if m.limiter != nil {
					// Estimate segment size for limiter (use ExpectLength if available, else 1 byte as minimum trigger)
					waitBytes := 1
					if seg.ExpectLength != nil && *seg.ExpectLength > 0 {
						waitBytes = int(*seg.ExpectLength)
					}
					if err := m.limiter.Wait(ctx, waitBytes); err != nil {
						results <- indexedResult{index: idx, result: DownloadResult{Segment: seg, Error: err}}
						continue
					}
				}
				result := sd.Download(ctx, seg, tempDir, idx)
				if result.Error == nil {
					tracker.AddBytes(result.Bytes)
					tracker.SegmentDone()
				}
				results <- indexedResult{index: idx, result: result}
			}
		}()
	}

	// Enqueue jobs
	for i := range allSegments {
		jobs <- i
	}
	close(jobs)

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	paths := make([]string, len(allSegments))
	var firstErr error
	for r := range results {
		if r.result.Error != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("segment %d: %w", r.index, r.result.Error)
			}
			continue
		}
		paths[r.index] = r.result.FilePath
	}

	if firstErr != nil {
		return nil, firstErr
	}

	// Final progress report
	if m.onProgress != nil {
		m.onProgress(tracker.Progress())
	}

	return paths, nil
}

// CleanupTemp removes the temp directory and all downloaded segments.
func CleanupTemp(tempDir string) error {
	return os.RemoveAll(tempDir)
}

// SegmentPath returns the expected path for a downloaded segment.
// index is the 0-based position index (not the segment's own Index field).
func SegmentPath(tempDir string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("seg_%d.ts", index))
}

// ---------------------------------------------------------------------------
// TaskManager — multi-task concurrent download orchestrator
// ---------------------------------------------------------------------------

// TaskResult holds the result of a completed task.
type TaskResult struct {
	TaskID     string
	Status     model.TaskStatus
	OutputPath string
	Error      error
	Duration   float64
}

// TaskState tracks individual task state.
type TaskState struct {
	ID        string
	Status    model.TaskStatus
	Progress  Progress
	StartTime time.Time
}

// TaskManager manages multiple concurrent download tasks.
type TaskManager struct {
	maxTasks   int
	sem        chan struct{}
	results    chan TaskResult
	mu         sync.Mutex
	tasks      map[string]*TaskState
	wg         sync.WaitGroup
	progressFn map[string]func(Progress)
}

// NewTaskManager creates a task manager with max concurrent tasks.
func NewTaskManager(maxConcurrent int) *TaskManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &TaskManager{
		maxTasks:   maxConcurrent,
		sem:        make(chan struct{}, maxConcurrent),
		results:    make(chan TaskResult, maxConcurrent*2),
		tasks:      make(map[string]*TaskState),
		progressFn: make(map[string]func(Progress)),
	}
}

// Submit submits a download function as a task. Returns taskID.
func (tm *TaskManager) Submit(taskID string, fn func(ctx context.Context) error) error {
	tm.mu.Lock()
	if _, exists := tm.tasks[taskID]; exists {
		tm.mu.Unlock()
		return fmt.Errorf("task %s already exists", taskID)
	}
	tm.tasks[taskID] = &TaskState{
		ID:        taskID,
		Status:    model.TaskStatusPending,
		StartTime: time.Now(),
	}
	tm.mu.Unlock()

	tm.wg.Add(1)
	go func() {
		defer tm.wg.Done()

		// Acquire semaphore
		select {
		case tm.sem <- struct{}{}:
		case <-context.Background().Done():
			// Should not happen without a real context; guard anyway
			return
		}
		defer func() { <-tm.sem }()

		tm.mu.Lock()
		tm.tasks[taskID].Status = model.TaskStatusDownloading
		tm.mu.Unlock()

		start := time.Now()
		err := fn(context.Background())
		dur := time.Since(start).Seconds()

		status := model.TaskStatusDone
		if err != nil {
			status = model.TaskStatusFailed
		}

		tm.mu.Lock()
		tm.tasks[taskID].Status = status
		outputPath := ""
		// Copy progress info to result
		tm.mu.Unlock()

		tm.results <- TaskResult{
			TaskID:     taskID,
			Status:     status,
			OutputPath: outputPath,
			Error:      err,
			Duration:   dur,
		}
	}()

	return nil
}

// SubmitWithContext submits a download function with a cancellable context.
func (tm *TaskManager) SubmitWithContext(ctx context.Context, taskID string, fn func(ctx context.Context) error) error {
	tm.mu.Lock()
	if _, exists := tm.tasks[taskID]; exists {
		tm.mu.Unlock()
		return fmt.Errorf("task %s already exists", taskID)
	}
	tm.tasks[taskID] = &TaskState{
		ID:        taskID,
		Status:    model.TaskStatusPending,
		StartTime: time.Now(),
	}
	tm.mu.Unlock()

	tm.wg.Add(1)
	go func() {
		defer tm.wg.Done()

		// Acquire semaphore or bail on context cancel
		select {
		case tm.sem <- struct{}{}:
		case <-ctx.Done():
			tm.mu.Lock()
			tm.tasks[taskID].Status = model.TaskStatusCancelled
			tm.mu.Unlock()
			tm.results <- TaskResult{
				TaskID: taskID,
				Status: model.TaskStatusCancelled,
				Error:  ctx.Err(),
			}
			return
		}
		defer func() { <-tm.sem }()

		tm.mu.Lock()
		tm.tasks[taskID].Status = model.TaskStatusDownloading
		tm.mu.Unlock()

		start := time.Now()
		err := fn(ctx)
		dur := time.Since(start).Seconds()

		status := model.TaskStatusDone
		if err != nil {
			if ctx.Err() != nil {
				status = model.TaskStatusCancelled
			} else {
				status = model.TaskStatusFailed
			}
		}

		tm.mu.Lock()
		tm.tasks[taskID].Status = status
		tm.mu.Unlock()

		tm.results <- TaskResult{
			TaskID:   taskID,
			Status:   status,
			Error:    err,
			Duration: dur,
		}
	}()

	return nil
}

// Wait waits for all tasks to complete and returns results.
func (tm *TaskManager) Wait(ctx context.Context) []TaskResult {
	// Close results channel once all tasks finish
	go func() {
		tm.wg.Wait()
		close(tm.results)
	}()

	var results []TaskResult
	for {
		select {
		case r, ok := <-tm.results:
			if !ok {
				return results
			}
			results = append(results, r)
		case <-ctx.Done():
			// Context cancelled — drain remaining results
			for r := range tm.results {
				results = append(results, r)
			}
			return results
		}
	}
}

// ActiveTasks returns currently running task states.
func (tm *TaskManager) ActiveTasks() []TaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	states := make([]TaskState, 0, len(tm.tasks))
	for _, t := range tm.tasks {
		states = append(states, TaskState{
			ID:        t.ID,
			Status:    t.Status,
			Progress:  t.Progress,
			StartTime: t.StartTime,
		})
	}
	return states
}

// OnProgress sets a callback for task progress updates.
func (tm *TaskManager) OnProgress(taskID string, fn func(Progress)) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.progressFn[taskID] = fn
}

// ReportProgress reports progress for a specific task (called by download functions).
func (tm *TaskManager) ReportProgress(taskID string, p Progress) {
	tm.mu.Lock()
	if state, ok := tm.tasks[taskID]; ok {
		state.Progress = p
	}
	fn := tm.progressFn[taskID]
	tm.mu.Unlock()

	if fn != nil {
		fn(p)
	}
}
