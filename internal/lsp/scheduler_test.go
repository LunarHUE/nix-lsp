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
