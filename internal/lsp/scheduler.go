package lsp

import (
	"context"
	"errors"
	"sync"
)

// Lane is a scheduler priority lane.
type Lane int

const (
	// LaneInteractive is for latency-sensitive requests such as hover and
	// completion.
	LaneInteractive Lane = iota
	// LaneResponsive is for user-visible work that can take a little longer,
	// such as references or code actions.
	LaneResponsive
	// LaneBackground is for indexing, diagnostics refreshes, and other work
	// that should yield to editor interactions.
	LaneBackground
)

// ErrSchedulerStopped is returned when work is submitted after shutdown.
var ErrSchedulerStopped = errors.New("lsp: scheduler stopped")

// Task is one scheduled unit of work.
type Task func(context.Context) error

// TaskResult is delivered once a scheduled task has either run or been
// canceled before starting.
type TaskResult struct {
	Err error
}

type scheduledTask struct {
	ctx    context.Context
	task   Task
	result chan TaskResult
}

// Scheduler runs LSP work with coarse priority lanes.
type Scheduler struct {
	queues [3]chan scheduledTask

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewScheduler creates a scheduler. Work may be submitted before Start.
func NewScheduler(queueSize int) *Scheduler {
	if queueSize <= 0 {
		queueSize = 1
	}

	s := &Scheduler{}
	for lane := range s.queues {
		s.queues[lane] = make(chan scheduledTask, queueSize)
	}
	return s
}

// Start launches workerCount workers.
func (s *Scheduler) Start(ctx context.Context, workerCount int) {
	if workerCount <= 0 {
		workerCount = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	for range workerCount {
		s.wg.Add(1)
		go s.worker(runCtx)
	}
}

// Submit queues a task in lane and returns a one-shot result channel.
func (s *Scheduler) Submit(ctx context.Context, lane Lane, task Task) <-chan TaskResult {
	result := make(chan TaskResult, 1)
	if task == nil {
		result <- TaskResult{}
		close(result)
		return result
	}
	if lane < LaneInteractive || lane > LaneBackground {
		lane = LaneBackground
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		result <- TaskResult{Err: ErrSchedulerStopped}
		close(result)
		return result
	}

	job := scheduledTask{ctx: ctx, task: task, result: result}
	select {
	case <-ctx.Done():
		result <- TaskResult{Err: ctx.Err()}
		close(result)
	case s.queues[lane] <- job:
	}
	return result
}

// Stop cancels workers and waits for them to exit.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	s.drainQueued()
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		job, ok := s.next(ctx)
		if !ok {
			return
		}
		s.run(ctx, job)
	}
}

func (s *Scheduler) next(ctx context.Context) (scheduledTask, bool) {
	for {
		select {
		case <-ctx.Done():
			return scheduledTask{}, false
		case job := <-s.queues[LaneInteractive]:
			return job, true
		default:
		}

		select {
		case <-ctx.Done():
			return scheduledTask{}, false
		case job := <-s.queues[LaneInteractive]:
			return job, true
		case job := <-s.queues[LaneResponsive]:
			return job, true
		default:
		}

		select {
		case <-ctx.Done():
			return scheduledTask{}, false
		case job := <-s.queues[LaneInteractive]:
			return job, true
		case job := <-s.queues[LaneResponsive]:
			return job, true
		case job := <-s.queues[LaneBackground]:
			return job, true
		}
	}
}

func (s *Scheduler) run(ctx context.Context, job scheduledTask) {
	defer close(job.result)

	select {
	case <-ctx.Done():
		job.result <- TaskResult{Err: ctx.Err()}
		return
	case <-job.ctx.Done():
		job.result <- TaskResult{Err: job.ctx.Err()}
		return
	default:
	}

	job.result <- TaskResult{Err: job.task(job.ctx)}
}

func (s *Scheduler) drainQueued() {
	for _, queue := range s.queues {
		draining := true
		for draining {
			select {
			case job := <-queue:
				job.result <- TaskResult{Err: ErrSchedulerStopped}
				close(job.result)
			default:
				draining = false
			}
		}
	}
}
