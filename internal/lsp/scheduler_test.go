package lsp

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSchedulerRunsHigherPriorityWorkFirstWhenQueued(t *testing.T) {
	scheduler := NewScheduler(8)
	defer scheduler.Stop()

	var mu sync.Mutex
	var order []string
	record := func(name string) Task {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, name)
			return nil
		}
	}

	bg := scheduler.Submit(context.Background(), LaneBackground, record("background"))
	interactive := scheduler.Submit(context.Background(), LaneInteractive, record("interactive"))

	scheduler.Start(context.Background(), 1)
	mustResult(t, interactive)
	mustResult(t, bg)

	if !reflect.DeepEqual(order, []string{"interactive", "background"}) {
		t.Fatalf("order = %v, want interactive before background", order)
	}
}

func TestSchedulerHonorsCanceledTaskContextBeforeRun(t *testing.T) {
	scheduler := NewScheduler(4)
	defer scheduler.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := scheduler.Submit(ctx, LaneInteractive, func(context.Context) error {
		t.Fatal("task should not run")
		return nil
	})
	scheduler.Start(context.Background(), 1)

	got := <-result
	if !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", got.Err)
	}
}

func TestSchedulerStopRejectsNewWork(t *testing.T) {
	scheduler := NewScheduler(1)
	scheduler.Start(context.Background(), 1)
	scheduler.Stop()

	got := <-scheduler.Submit(context.Background(), LaneInteractive, func(context.Context) error {
		return nil
	})
	if !errors.Is(got.Err, ErrSchedulerStopped) {
		t.Fatalf("err = %v, want ErrSchedulerStopped", got.Err)
	}
}

func TestSchedulerStopDrainsQueuedWork(t *testing.T) {
	scheduler := NewScheduler(2)
	result := scheduler.Submit(context.Background(), LaneBackground, func(context.Context) error {
		t.Fatal("queued task should not run")
		return nil
	})

	scheduler.Stop()

	got := <-result
	if !errors.Is(got.Err, ErrSchedulerStopped) {
		t.Fatalf("err = %v, want ErrSchedulerStopped", got.Err)
	}
}

// TestTrySubmitReportsQueueFullWithoutBlocking is the unit-level guard for the
// stuck-diagnostics freeze: with a lane queue full and no worker draining it,
// TrySubmit must return immediately (never block) and report the overflow, where
// the blocking Submit would park the caller forever. A parked caller on the LSP
// read loop is exactly what wedged the whole server before this fix.
func TestTrySubmitReportsQueueFullWithoutBlocking(t *testing.T) {
	scheduler := NewScheduler(4) // queue cap 4, workers never started so nothing drains
	defer scheduler.Stop()

	noop := func(context.Context) error { return nil }
	for range 4 {
		if _, ok := scheduler.TrySubmit(context.Background(), LaneBackground, noop); !ok {
			t.Fatal("TrySubmit reported full before the queue was filled")
		}
	}

	done := make(chan TaskResult, 1)
	go func() {
		result, ok := scheduler.TrySubmit(context.Background(), LaneBackground, noop)
		if ok {
			t.Errorf("TrySubmit ok = true on a full queue, want false")
		}
		done <- <-result
	}()

	select {
	case got := <-done:
		if !errors.Is(got.Err, ErrQueueFull) {
			t.Fatalf("err = %v, want ErrQueueFull", got.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("TrySubmit blocked on a full queue; it must be non-blocking")
	}
}

// TestTrySubmitEnqueuesAndRunsWhenRoomAvailable confirms the accepted path still
// runs the task and delivers its result.
func TestTrySubmitEnqueuesAndRunsWhenRoomAvailable(t *testing.T) {
	scheduler := NewScheduler(4)
	scheduler.Start(context.Background(), 1)
	defer scheduler.Stop()

	ran := make(chan struct{})
	result, ok := scheduler.TrySubmit(context.Background(), LaneBackground, func(context.Context) error {
		close(ran)
		return nil
	})
	if !ok {
		t.Fatal("TrySubmit ok = false with an empty queue, want true")
	}
	mustResult(t, result)
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("task did not run")
	}
}

func mustResult(t *testing.T, result <-chan TaskResult) {
	t.Helper()
	select {
	case got := <-result:
		if got.Err != nil {
			t.Fatalf("task error = %v", got.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled task")
	}
}
