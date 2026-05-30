package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ResumeState persists download state for resumable downloads.
type ResumeState struct {
	TaskID        string         `json:"task_id"`
	URL           string         `json:"url"`
	OutputPath    string         `json:"output_path"`
	TotalSegments int            `json:"total_segments"`
	Completed     map[int]string `json:"completed"` // index -> filepath
	StartedAt     time.Time      `json:"started_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// NewResumeState creates a new resume state.
func NewResumeState(taskID, url, outputPath string, totalSegments int) *ResumeState {
	now := time.Now()
	return &ResumeState{
		TaskID:        taskID,
		URL:           url,
		OutputPath:    outputPath,
		TotalSegments: totalSegments,
		Completed:     make(map[int]string),
		StartedAt:     now,
		UpdatedAt:     now,
	}
}

// statePath returns the path to the resume state file.
func statePath(tempDir string) string {
	return filepath.Join(tempDir, ".resume_state.json")
}

// SaveResumeState saves the resume state to disk.
func SaveResumeState(state *ResumeState, tempDir string) error {
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	path := statePath(tempDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadResumeState loads a resume state from disk.
func LoadResumeState(tempDir string) (*ResumeState, error) {
	path := statePath(tempDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var state ResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &state, nil
}

// HasResumeState checks if a resume state exists.
func HasResumeState(tempDir string) bool {
	_, err := os.Stat(statePath(tempDir))
	return err == nil
}

// ClearResumeState removes the resume state file.
func ClearResumeState(tempDir string) error {
	return os.Remove(statePath(tempDir))
}

// MarkCompleted marks a segment as completed.
func (rs *ResumeState) MarkCompleted(index int, filePath string) {
	rs.Completed[index] = filePath
	rs.UpdatedAt = time.Now()
}

// IsCompleted checks if a segment has been completed.
func (rs *ResumeState) IsCompleted(index int) bool {
	_, ok := rs.Completed[index]
	return ok
}

// PendingSegments returns the indices of segments that haven't been completed.
func (rs *ResumeState) PendingSegments() []int {
	var pending []int
	for i := 0; i < rs.TotalSegments; i++ {
		if !rs.IsCompleted(i) {
			pending = append(pending, i)
		}
	}
	return pending
}

// Progress returns the completion percentage.
func (rs *ResumeState) Progress() float64 {
	if rs.TotalSegments == 0 {
		return 0
	}
	return float64(len(rs.Completed)) / float64(rs.TotalSegments) * 100
}
