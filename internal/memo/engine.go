// Package memo provides tracked query execution with automatic dependency
// recording and lazy invalidation.
package memo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Key identifies one query value.
type Key struct {
	Kind string
	ID   string
}

// String returns a diagnostic representation of the key.
func (k Key) String() string {
	return k.Kind + ":" + k.ID
}

// QueryFunc computes a key. Dependencies are recorded automatically when the
// function reads other keys through Context.Get.
type QueryFunc func(context.Context, *Context, Key) (any, error)

// ErrCycle is returned when tracked query execution detects a dependency cycle.
var ErrCycle = errors.New("memo: dependency cycle")

// ErrNoQuery is returned for a key whose kind is neither registered nor set as
// an input.
var ErrNoQuery = errors.New("memo: no query registered")

// Engine stores input and derived query values.
type Engine struct {
	mu sync.RWMutex

	queries map[string]QueryFunc
	entries map[Key]entry
	reverse map[Key]map[Key]struct{}

	recomputes map[Key]uint64
}

type entry struct {
	value any
	deps  map[Key]struct{}
	dirty bool
	input bool
}

// Context is passed to query functions and records dependency reads.
type Context struct {
	engine  *Engine
	current Key
	stack   map[Key]struct{}
	deps    map[Key]struct{}
}

// Stats captures observable engine behavior for tests and diagnostics.
type Stats struct {
	Entries    int
	Recomputes map[Key]uint64
}

// New creates an empty memo engine.
func New() *Engine {
	return &Engine{
		queries:    make(map[string]QueryFunc),
		entries:    make(map[Key]entry),
		reverse:    make(map[Key]map[Key]struct{}),
		recomputes: make(map[Key]uint64),
	}
}

// Register installs a query function for kind.
func (e *Engine) Register(kind string, query QueryFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queries[kind] = query
}

// SetInput stores an input value and marks transitive dependents dirty when the
// value changes.
func (e *Engine) SetInput(key Key, value any) {
	e.mu.Lock()
	defer e.mu.Unlock()

	old, ok := e.entries[key]
	if ok && !old.dirty && reflect.DeepEqual(old.value, value) {
		return
	}
	e.removeDepsLocked(key)
	e.entries[key] = entry{value: value, deps: map[Key]struct{}{}, input: true}
	e.markDependentsDirtyLocked(key)
}

// Get returns key's value, recomputing it lazily when dirty.
func (e *Engine) Get(ctx context.Context, key Key) (any, error) {
	return e.get(ctx, key, nil)
}

func (c *Context) Get(ctx context.Context, key Key) (any, error) {
	if c != nil {
		c.deps[key] = struct{}{}
	}
	return c.engine.get(ctx, key, c.stack)
}

// Dependencies returns a snapshot of key's recorded dependencies.
func (e *Engine) Dependencies(key Key) []Key {
	e.mu.RLock()
	defer e.mu.RUnlock()

	item, ok := e.entries[key]
	if !ok {
		return nil
	}
	deps := make([]Key, 0, len(item.deps))
	for dep := range item.deps {
		deps = append(deps, dep)
	}
	return deps
}

// Stats returns a snapshot of memo counters.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	recomputes := make(map[Key]uint64, len(e.recomputes))
	for key, count := range e.recomputes {
		recomputes[key] = count
	}
	return Stats{Entries: len(e.entries), Recomputes: recomputes}
}

func (e *Engine) get(ctx context.Context, key Key, stack map[Key]struct{}) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := stack[key]; ok {
		return nil, fmt.Errorf("%w: %s", ErrCycle, key)
	}

	e.mu.RLock()
	item, hasEntry := e.entries[key]
	query := e.queries[key.Kind]
	if hasEntry && !item.dirty {
		e.mu.RUnlock()
		return item.value, nil
	}
	if query == nil {
		e.mu.RUnlock()
		if hasEntry {
			return item.value, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNoQuery, key.Kind)
	}
	e.mu.RUnlock()

	nextStack := cloneStack(stack)
	nextStack[key] = struct{}{}
	queryCtx := &Context{
		engine:  e,
		current: key,
		stack:   nextStack,
		deps:    make(map[Key]struct{}),
	}

	value, err := query(ctx, queryCtx, key)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.removeDepsLocked(key)
	e.entries[key] = entry{value: value, deps: queryCtx.deps}
	for dep := range queryCtx.deps {
		if e.reverse[dep] == nil {
			e.reverse[dep] = make(map[Key]struct{})
		}
		e.reverse[dep][key] = struct{}{}
	}
	e.recomputes[key]++
	return value, nil
}

func (e *Engine) removeDepsLocked(key Key) {
	old, ok := e.entries[key]
	if !ok {
		return
	}
	for dep := range old.deps {
		delete(e.reverse[dep], key)
		if len(e.reverse[dep]) == 0 {
			delete(e.reverse, dep)
		}
	}
}

func (e *Engine) markDependentsDirtyLocked(key Key) {
	seen := make(map[Key]struct{})
	queue := []Key{key}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for dependent := range e.reverse[current] {
			if _, ok := seen[dependent]; ok {
				continue
			}
			seen[dependent] = struct{}{}
			item := e.entries[dependent]
			item.dirty = true
			e.entries[dependent] = item
			queue = append(queue, dependent)
		}
	}
}

func cloneStack(stack map[Key]struct{}) map[Key]struct{} {
	cloned := make(map[Key]struct{}, len(stack)+1)
	for key := range stack {
		cloned[key] = struct{}{}
	}
	return cloned
}
