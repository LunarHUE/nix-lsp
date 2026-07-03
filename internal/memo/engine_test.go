package memo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestTrackedDependenciesAreRecorded(t *testing.T) {
	engine := New()
	source := Key{Kind: "input", ID: "source"}
	derived := Key{Kind: "derived", ID: "value"}
	engine.SetInput(source, 41)
	engine.Register("derived", func(ctx context.Context, q *Context, _ Key) (any, error) {
		value, err := q.Get(ctx, source)
		if err != nil {
			return nil, err
		}
		return value.(int) + 1, nil
	})

	got, err := engine.Get(context.Background(), derived)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got != 42 {
		t.Fatalf("value = %v, want 42", got)
	}

	deps := engine.Dependencies(derived)
	if len(deps) != 1 || deps[0] != source {
		t.Fatalf("deps = %v, want [%v]", deps, source)
	}
}

func TestInvalidationFanOutIsLazyAndExact(t *testing.T) {
	engine := New()
	source := Key{Kind: "input", ID: "source"}
	mid := Key{Kind: "mid", ID: "one"}
	leaf := Key{Kind: "leaf", ID: "one"}
	unrelated := Key{Kind: "unrelated", ID: "one"}

	engine.SetInput(source, 1)
	engine.Register("mid", func(ctx context.Context, q *Context, _ Key) (any, error) {
		value, err := q.Get(ctx, source)
		if err != nil {
			return nil, err
		}
		return value.(int) * 2, nil
	})
	engine.Register("leaf", func(ctx context.Context, q *Context, _ Key) (any, error) {
		value, err := q.Get(ctx, mid)
		if err != nil {
			return nil, err
		}
		return value.(int) + 1, nil
	})
	engine.Register("unrelated", func(context.Context, *Context, Key) (any, error) {
		return 99, nil
	})

	mustGet(t, engine, leaf)
	mustGet(t, engine, unrelated)
	engine.SetInput(source, 2)
	mustGet(t, engine, leaf)

	stats := engine.Stats()
	if stats.Recomputes[mid] != 2 {
		t.Fatalf("mid recomputes = %d, want 2", stats.Recomputes[mid])
	}
	if stats.Recomputes[leaf] != 2 {
		t.Fatalf("leaf recomputes = %d, want 2", stats.Recomputes[leaf])
	}
	if stats.Recomputes[unrelated] != 1 {
		t.Fatalf("unrelated recomputes = %d, want 1", stats.Recomputes[unrelated])
	}
}

func TestDependencyReplacement(t *testing.T) {
	engine := New()
	oldInput := Key{Kind: "input", ID: "old"}
	newInput := Key{Kind: "input", ID: "new"}
	selector := Key{Kind: "input", ID: "selector"}
	derived := Key{Kind: "derived", ID: "value"}

	engine.SetInput(oldInput, 1)
	engine.SetInput(newInput, 10)
	engine.SetInput(selector, "old")
	engine.Register("derived", func(ctx context.Context, q *Context, _ Key) (any, error) {
		selected, err := q.Get(ctx, selector)
		if err != nil {
			return nil, err
		}
		if selected == "old" {
			return q.Get(ctx, oldInput)
		}
		return q.Get(ctx, newInput)
	})

	if got := mustGet(t, engine, derived); got != 1 {
		t.Fatalf("derived = %v, want 1", got)
	}
	engine.SetInput(selector, "new")
	if got := mustGet(t, engine, derived); got != 10 {
		t.Fatalf("derived = %v, want 10", got)
	}
	engine.SetInput(oldInput, 2)
	if got := mustGet(t, engine, derived); got != 10 {
		t.Fatalf("derived after old input change = %v, want cached 10", got)
	}

	stats := engine.Stats()
	if stats.Recomputes[derived] != 2 {
		t.Fatalf("derived recomputes = %d, want 2", stats.Recomputes[derived])
	}
}

func TestCycleDetection(t *testing.T) {
	engine := New()
	a := Key{Kind: "cycle", ID: "a"}
	b := Key{Kind: "cycle", ID: "b"}
	engine.Register("cycle", func(ctx context.Context, q *Context, key Key) (any, error) {
		if key.ID == "a" {
			return q.Get(ctx, b)
		}
		return q.Get(ctx, a)
	})

	_, err := engine.Get(context.Background(), a)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
}

func TestConcurrentReadSafety(t *testing.T) {
	engine := New()
	input := Key{Kind: "input", ID: "value"}
	derived := Key{Kind: "derived", ID: "value"}
	engine.SetInput(input, 1)
	engine.Register("derived", func(ctx context.Context, q *Context, _ Key) (any, error) {
		value, err := q.Get(ctx, input)
		if err != nil {
			return nil, err
		}
		return value.(int) + 1, nil
	})

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 100 {
				if worker%2 == 0 && i%20 == 0 {
					engine.SetInput(input, i)
				}
				value, err := engine.Get(context.Background(), derived)
				if err != nil {
					errs <- err
					return
				}
				if _, ok := value.(int); !ok {
					errs <- fmt.Errorf("value type = %T, want int", value)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestForgetDropsMatchingEntriesAndReverseEdges(t *testing.T) {
	engine := New()
	shared := Key{Kind: "input", ID: "shared"}
	oldInput := Key{Kind: "FileInput", ID: "old"}
	newInput := Key{Kind: "FileInput", ID: "new"}
	oldDerived := Key{Kind: "derived", ID: "old"}
	newDerived := Key{Kind: "derived", ID: "new"}

	engine.SetInput(shared, 100)
	engine.SetInput(oldInput, 1)
	engine.SetInput(newInput, 2)
	engine.Register("derived", func(ctx context.Context, q *Context, key Key) (any, error) {
		base, err := q.Get(ctx, Key{Kind: "FileInput", ID: key.ID})
		if err != nil {
			return nil, err
		}
		bonus, err := q.Get(ctx, shared)
		if err != nil {
			return nil, err
		}
		return base.(int) + bonus.(int), nil
	})

	mustGet(t, engine, oldDerived)
	mustGet(t, engine, newDerived)

	// Forget everything belonging to the "old" identity: its input and derived.
	engine.Forget(func(key Key) bool { return key.ID == "old" })

	if got := engine.Len(); got != 3 {
		t.Fatalf("Len after Forget = %d, want 3 (shared + new input + new derived)", got)
	}
	if _, err := engine.Get(context.Background(), newDerived); err != nil {
		t.Fatalf("surviving newDerived Get error = %v", err)
	}

	// The reverse edge from shared to oldDerived must be gone: bumping shared
	// must not resurrect or dirty a forgotten key. Recompute the survivor and
	// confirm it (not the forgotten one) recomputed.
	before := engine.Stats().Recomputes[newDerived]
	engine.SetInput(shared, 200)
	if got := mustGet(t, engine, newDerived); got != 202 {
		t.Fatalf("newDerived after shared bump = %v, want 202", got)
	}
	if got := engine.Stats().Recomputes[newDerived]; got != before+1 {
		t.Fatalf("newDerived recomputes = %d, want %d", got, before+1)
	}
	// The forgotten key's recompute counter was cleared, and a fresh Get recomputes
	// it from scratch (input still present as it was re-set? no — it was forgotten).
	if _, ok := engine.Stats().Recomputes[oldDerived]; ok {
		t.Fatalf("forgotten oldDerived recompute counter survived: %v", engine.Stats().Recomputes)
	}
}

// TestForgetInputThenReadRecomputesOrMisses documents what a concurrent reader of
// a forgotten key experiences: a derived key with a registered query recomputes
// cleanly, while a plain input key with no query surfaces ErrNoQuery — never a
// panic or a stale wrong value.
func TestForgetInputThenReadRecomputesOrMisses(t *testing.T) {
	engine := New()
	input := Key{Kind: "FileInput", ID: "x"}
	derived := Key{Kind: "derived", ID: "x"}
	engine.SetInput(input, 5)
	engine.Register("derived", func(ctx context.Context, q *Context, key Key) (any, error) {
		v, err := q.Get(ctx, Key{Kind: "FileInput", ID: key.ID})
		if err != nil {
			return nil, err
		}
		return v.(int) * 2, nil
	})
	mustGet(t, engine, derived)

	// Forget only the derived entry (input remains): reading it must recompute.
	engine.Forget(func(key Key) bool { return key.Kind == "derived" })
	if got := mustGet(t, engine, derived); got != 10 {
		t.Fatalf("derived after Forget = %v, want recompute to 10", got)
	}

	// Forget the input too: a plain input key has no registered query, so reading
	// it surfaces ErrNoQuery rather than panicking.
	engine.Forget(func(key Key) bool { return key.Kind == "FileInput" })
	if _, err := engine.Get(context.Background(), input); !errors.Is(err, ErrNoQuery) {
		t.Fatalf("Get(forgotten input) error = %v, want ErrNoQuery", err)
	}
}

func mustGet(t *testing.T, engine *Engine, key Key) any {
	t.Helper()
	value, err := engine.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%v) error = %v", key, err)
	}
	return value
}
