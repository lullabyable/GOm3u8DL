package downloader

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

func TestNewTaskManager(t *testing.T) {
	tm := NewTaskManager(4)
	if tm.maxTasks != 4 {
		t.Errorf("maxTasks = %d, want 4", tm.maxTasks)
	}
	if cap(tm.sem) != 4 {
		t.Errorf("sem capacity = %d, want 4", cap(tm.sem))
	}
}

func TestNewTaskManagerZeroDefaults(t *testing.T) {
	tm := NewTaskManager(0)
	if tm.maxTasks != 1 {
		t.Errorf("maxTasks = %d, want 1 (default)", tm.maxTasks)
	}
}

func TestTaskManagerSubmitAndWait(t *testing.T) {
	tm := NewTaskManager(2)

	err := tm.Submit("task1", func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	err = tm.Submit("task2", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	results := tm.Wait(context.Background())
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Error != nil {
			t.Errorf("task %s failed: %v", r.TaskID, r.Error)
		}
		if r.Status != model.TaskStatusDone {
			t.Errorf("task %s status = %s, want done", r.TaskID, r.Status)
		}
		if r.Duration <= 0 {
			t.Errorf("task %s duration = %f, want > 0", r.TaskID, r.Duration)
		}
	}
}

func TestTaskManagerDuplicateTask(t *testing.T) {
	tm := NewTaskManager(2)

	err := tm.Submit("task1", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	err = tm.Submit("task1", func(ctx context.Context) error { return nil })
	if err == nil {
		t.Error("expected error for duplicate task ID")
	}

	// Wait to clean up
	tm.Wait(context.Background())
}

func TestTaskManagerFailedTask(t *testing.T) {
	tm := NewTaskManager(2)

	testErr := errors.New("download failed")
	err := tm.Submit("fail1", func(ctx context.Context) error {
		return testErr
	})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	results := tm.Wait(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Status != model.TaskStatusFailed {
		t.Errorf("status = %s, want failed", results[0].Status)
	}
	if results[0].Error == nil {
		t.Error("expected non-nil error")
	}
}

func TestTaskManagerConcurrencyLimit(t *testing.T) {
	tm := NewTaskManager(2) // max 2 concurrent

	var running int32
	var maxRunning int32

	for i := 0; i < 10; i++ {
		tm.Submit("task"+string(rune('A'+i)), func(ctx context.Context) error {
			cur := atomic.AddInt32(&running, 1)
			// Track max concurrent
			for {
				old := atomic.LoadInt32(&maxRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return nil
		})
	}

	tm.Wait(context.Background())

	if maxRunning > 2 {
		t.Errorf("max concurrent tasks = %d, want <= 2", maxRunning)
	}
}

func TestTaskManagerContextCancel(t *testing.T) {
	tm := NewTaskManager(2)

	ctx, cancel := context.WithCancel(context.Background())
	err := tm.SubmitWithContext(ctx, "cancel1", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	results := tm.Wait(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Status != model.TaskStatusCancelled {
		t.Errorf("status = %s, want cancelled", results[0].Status)
	}
}

func TestTaskManagerActiveTasks(t *testing.T) {
	tm := NewTaskManager(2)

	started := make(chan struct{})
	tm.Submit("active1", func(ctx context.Context) error {
		close(started)
		time.Sleep(200 * time.Millisecond)
		return nil
	})

	<-started
	time.Sleep(10 * time.Millisecond) // let state update

	tasks := tm.ActiveTasks()
	found := false
	for _, ts := range tasks {
		if ts.ID == "active1" {
			found = true
			if ts.Status != model.TaskStatusDownloading {
				t.Errorf("status = %s, want downloading", ts.Status)
			}
		}
	}
	if !found {
		t.Error("active1 not found in ActiveTasks")
	}

	tm.Wait(context.Background())
}

func TestTaskManagerOnProgress(t *testing.T) {
	tm := NewTaskManager(2)

	var received Progress
	tm.OnProgress("prog1", func(p Progress) {
		received = p
	})

	tm.Submit("prog1", func(ctx context.Context) error {
		tm.ReportProgress("prog1", Progress{
			Downloaded: 500,
			Total:      1000,
			Percent:    50.0,
		})
		return nil
	})

	tm.Wait(context.Background())

	if received.Downloaded != 500 {
		t.Errorf("progress Downloaded = %d, want 500", received.Downloaded)
	}
	if received.Percent != 50.0 {
		t.Errorf("progress Percent = %f, want 50.0", received.Percent)
	}
}
