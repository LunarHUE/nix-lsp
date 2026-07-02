package memo

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGetOrComputeCachesHits(t *testing.T) {
	cache := New[string, int]()
	var calls atomic.Int64

	compute := func() (int, []string, error) {
		return int(calls.Add(1)), []string{"source"}, nil
	}

	first, err := cache.GetOrCompute("derived", compute)
	if err != nil {
		t.Fatalf("first compute failed: %v", err)
	}
	second, err := cache.GetOrCompute("derived", compute)
	if err != nil {
		t.Fatalf("second compute failed: %v", err)
	}

	if first != 1 || second != 1 {
		t.Fatalf("cached values = %d, %d; want both 1", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("compute calls = %d; want 1", calls.Load())
	}

	stats := cache.Stats()
	if stats.Recomputes != 1 || stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("stats = %+v; want 1 recompute, 1 hit, 1 miss", stats)
	}
}

func TestInvalidateFansOutToDependents(t *testing.T) {
	cache := New[string, int]()
	cache.Set("source", 1)
	cache.Set("mid", 2, "source")
	cache.Set("leaf", 3, "mid")
	cache.Set("sibling", 4, "source")
	cache.Set("unrelated", 5)

	removed := cache.Invalidate("source")
	assertSameKeys(t, removed, []string{"source", "mid", "leaf", "sibling"})

	for _, key := range []string{"source", "mid", "leaf", "sibling"} {
		if _, ok := cache.Get(key); ok {
			t.Fatalf("key %q still cached after invalidation", key)
		}
	}
	if value, ok := cache.Get("unrelated"); !ok || value != 5 {
		t.Fatalf("unrelated value = %d, %v; want 5, true", value, ok)
	}
}

func TestSetReplacesDependencies(t *testing.T) {
	cache := New[string, int]()
	cache.Set("derived", 1, "old")
	cache.Set("derived", 2, "new")

	if removed := cache.Invalidate("old"); len(removed) != 0 {
		t.Fatalf("invalidating old dep removed %v; want none", removed)
	}
	if value, ok := cache.Get("derived"); !ok || value != 2 {
		t.Fatalf("derived after old invalidation = %d, %v; want 2, true", value, ok)
	}

	removed := cache.Invalidate("new")
	assertSameKeys(t, removed, []string{"derived"})
	if _, ok := cache.Get("derived"); ok {
		t.Fatal("derived still cached after new dependency invalidation")
	}
}

func TestConcurrentGetOrComputeSharesInFlightWork(t *testing.T) {
	cache := New[string, int]()
	start := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64

	compute := func() (int, []string, error) {
		calls.Add(1)
		close(start)
		<-release
		return 42, []string{"source"}, nil
	}

	const readers = 16
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := cache.GetOrCompute("derived", compute)
			if err != nil {
				errs <- err
				return
			}
			if value != 42 {
				errs <- errors.New("unexpected cached value")
			}
		}()
	}

	<-start
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("compute calls = %d; want 1", calls.Load())
	}
	if stats := cache.Stats(); stats.Recomputes != 1 || stats.Entries != 1 {
		t.Fatalf("stats = %+v; want 1 recompute and 1 entry", stats)
	}
}

func TestConcurrentReadsWritesAndInvalidation(t *testing.T) {
	cache := New[int, int]()
	var wg sync.WaitGroup

	for writer := range 8 {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := range 200 {
				key := writer*1000 + i
				cache.Set(key, i, writer)
				cache.Get(key)
				if i%25 == 0 {
					cache.Invalidate(writer)
				}
			}
		}(writer)
	}

	for reader := range 8 {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			for i := range 200 {
				cache.Get(reader*1000 + i)
				_ = cache.Stats()
			}
		}(reader)
	}

	wg.Wait()
}

func assertSameKeys(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("keys = %v; want %v", got, want)
	}

	counts := make(map[string]int, len(got))
	for _, key := range got {
		counts[key]++
	}
	for _, key := range want {
		counts[key]--
		if counts[key] < 0 {
			t.Fatalf("keys = %v; want %v", got, want)
		}
	}
}
