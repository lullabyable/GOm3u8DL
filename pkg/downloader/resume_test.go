package downloader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeStateSaveLoad(t *testing.T) {
	dir := t.TempDir()

	state := NewResumeState("test-task", "https://example.com/v.m3u8", "/tmp/output.mp4", 10)
	state.MarkCompleted(0, "/tmp/seg_0.ts")
	state.MarkCompleted(1, "/tmp/seg_1.ts")
	state.MarkCompleted(5, "/tmp/seg_5.ts")

	if err := SaveResumeState(state, dir); err != nil {
		t.Fatalf("SaveResumeState: %v", err)
	}

	loaded, err := LoadResumeState(dir)
	if err != nil {
		t.Fatalf("LoadResumeState: %v", err)
	}

	if loaded.TaskID != "test-task" {
		t.Errorf("TaskID = %q, want test-task", loaded.TaskID)
	}
	if loaded.TotalSegments != 10 {
		t.Errorf("TotalSegments = %d, want 10", loaded.TotalSegments)
	}
	if len(loaded.Completed) != 3 {
		t.Errorf("Completed = %d, want 3", len(loaded.Completed))
	}
	if loaded.Completed[0] != "/tmp/seg_0.ts" {
		t.Errorf("Completed[0] = %q", loaded.Completed[0])
	}
}

func TestResumeStateHasClear(t *testing.T) {
	dir := t.TempDir()

	if HasResumeState(dir) {
		t.Error("should not have resume state before save")
	}

	state := NewResumeState("task", "url", "out", 5)
	SaveResumeState(state, dir)

	if !HasResumeState(dir) {
		t.Error("should have resume state after save")
	}

	ClearResumeState(dir)

	if HasResumeState(dir) {
		t.Error("should not have resume state after clear")
	}
}

func TestResumeStatePendingSegments(t *testing.T) {
	state := NewResumeState("task", "url", "out", 5)
	state.MarkCompleted(0, "a")
	state.MarkCompleted(2, "c")
	state.MarkCompleted(4, "e")

	pending := state.PendingSegments()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	if pending[0] != 1 || pending[1] != 3 {
		t.Errorf("pending = %v, want [1 3]", pending)
	}
}

func TestResumeStateProgress(t *testing.T) {
	state := NewResumeState("task", "url", "out", 4)
	state.MarkCompleted(0, "a")
	state.MarkCompleted(1, "b")

	p := state.Progress()
	if p != 50.0 {
		t.Errorf("Progress = %f, want 50.0", p)
	}
}

func TestResumeStateNoTotal(t *testing.T) {
	state := NewResumeState("task", "url", "out", 0)
	if state.Progress() != 0 {
		t.Errorf("Progress = %f, want 0", state.Progress())
	}
}

func TestResumeStateIsCompleted(t *testing.T) {
	state := NewResumeState("task", "url", "out", 5)
	state.MarkCompleted(3, "d")

	if state.IsCompleted(3) != true {
		t.Error("expected segment 3 to be completed")
	}
	if state.IsCompleted(2) != false {
		t.Error("expected segment 2 to not be completed")
	}
}

func TestResumeStateFilePaths(t *testing.T) {
	dir := t.TempDir()
	state := NewResumeState("task", "url", "out", 5)
	SaveResumeState(state, dir)

	path := statePath(dir)
	if filepath.Base(path) != ".resume_state.json" {
		t.Errorf("state filename = %q, want .resume_state.json", filepath.Base(path))
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not found: %v", err)
	}
}
