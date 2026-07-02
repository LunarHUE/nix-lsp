package memo

import "sync"

// ComputeFunc builds a value and returns the keys it depends on.
type ComputeFunc[K comparable, V any] func() (V, []K, error)

// Stats captures observable cache behavior for tests and diagnostics.
type Stats struct {
	Entries    int
	Recomputes uint64
	Hits       uint64
	Misses     uint64
}

// Cache is a small in-memory memo table for content-hash keyed facts.
//
// Dependencies point from a derived value to the keys it used. Invalidation
// walks the reverse graph so changes to a source key evict all cached facts
// that transitively depend on it.
type Cache[K comparable, V any] struct {
	mu sync.RWMutex

	entries    map[K]entry[K, V]
	dependents map[K]map[K]struct{}
	inflight   map[K]*call[V]

	recomputes uint64
	hits       uint64
	misses     uint64
}

type entry[K comparable, V any] struct {
	value V
	deps  map[K]struct{}
}

type call[V any] struct {
	wg  sync.WaitGroup
	val V
	err error
}

// New returns an empty cache.
func New[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{
		entries:    make(map[K]entry[K, V]),
		dependents: make(map[K]map[K]struct{}),
		inflight:   make(map[K]*call[V]),
	}
}

// Get returns the cached value for key, if present.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.entries[key]
	if ok {
		c.hits++
		return item.value, true
	}

	c.misses++
	var zero V
	return zero, false
}

// Set stores value for key and replaces any existing dependency edges.
func (c *Cache[K, V]) Set(key K, value V, deps ...K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.setLocked(key, value, deps)
}

// GetOrCompute returns a cached value or computes, stores, and returns it.
//
// Concurrent callers for the same key share a single in-flight computation.
// Failed computations are not cached.
func (c *Cache[K, V]) GetOrCompute(key K, compute ComputeFunc[K, V]) (V, error) {
	c.mu.Lock()
	if item, ok := c.entries[key]; ok {
		c.hits++
		c.mu.Unlock()
		return item.value, nil
	}
	c.misses++

	if active, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		active.wg.Wait()
		return active.val, active.err
	}

	active := &call[V]{}
	active.wg.Add(1)
	c.inflight[key] = active
	c.recomputes++
	c.mu.Unlock()

	val, deps, err := compute()

	c.mu.Lock()
	if err == nil {
		c.setLocked(key, val, deps)
	}
	delete(c.inflight, key)
	active.val = val
	active.err = err
	active.wg.Done()
	c.mu.Unlock()

	return val, err
}

// Invalidate removes key and all cached values that transitively depend on it.
// The returned slice contains the removed cached keys in invalidation order.
func (c *Cache[K, V]) Invalidate(key K) []K {
	c.mu.Lock()
	defer c.mu.Unlock()

	var removed []K
	seen := make(map[K]struct{})
	queue := []K{key}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}

		for dependent := range c.dependents[current] {
			queue = append(queue, dependent)
		}

		if _, ok := c.entries[current]; ok {
			c.deleteLocked(current)
			removed = append(removed, current)
		}
	}

	return removed
}

// Dependencies returns a snapshot of the keys that key currently depends on.
func (c *Cache[K, V]) Dependencies(key K) []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.entries[key]
	if !ok {
		return nil
	}

	deps := make([]K, 0, len(item.deps))
	for dep := range item.deps {
		deps = append(deps, dep)
	}
	return deps
}

// Stats returns a snapshot of cache counters.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return Stats{
		Entries:    len(c.entries),
		Recomputes: c.recomputes,
		Hits:       c.hits,
		Misses:     c.misses,
	}
}

func (c *Cache[K, V]) setLocked(key K, value V, deps []K) {
	c.deleteLocked(key)

	depSet := make(map[K]struct{}, len(deps))
	for _, dep := range deps {
		depSet[dep] = struct{}{}
		if c.dependents[dep] == nil {
			c.dependents[dep] = make(map[K]struct{})
		}
		c.dependents[dep][key] = struct{}{}
	}

	c.entries[key] = entry[K, V]{
		value: value,
		deps:  depSet,
	}
}

func (c *Cache[K, V]) deleteLocked(key K) {
	item, ok := c.entries[key]
	if !ok {
		return
	}

	for dep := range item.deps {
		delete(c.dependents[dep], key)
		if len(c.dependents[dep]) == 0 {
			delete(c.dependents, dep)
		}
	}

	delete(c.entries, key)
}
