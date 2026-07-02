package server

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

const (
	diagnosticDebounce = 150 * time.Millisecond
	diagnosticRate     = 5 * time.Millisecond
)

type diagnosticUpdate struct {
	URI         string
	Diagnostics []syntax.Diagnostic
	Generation  uint64
	Debounce    bool
}

type diagnosticsPublisher struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	updates chan diagnosticUpdate

	mu       sync.RWMutex
	notifier lsp.Notifier
}

func newDiagnosticsPublisher() *diagnosticsPublisher {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := &diagnosticsPublisher{
		ctx:     ctx,
		cancel:  cancel,
		updates: make(chan diagnosticUpdate, 256),
	}
	publisher.wg.Add(1)
	go publisher.run()
	return publisher
}

func (p *diagnosticsPublisher) SetNotifier(notifier lsp.Notifier) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifier = notifier
}

func (p *diagnosticsPublisher) Publish(update diagnosticUpdate) {
	update.Diagnostics = cloneDiagnostics(update.Diagnostics)
	select {
	case <-p.ctx.Done():
	case p.updates <- update:
	}
}

func (p *diagnosticsPublisher) Stop() {
	p.cancel()
	p.wg.Wait()
}

func (p *diagnosticsPublisher) run() {
	defer p.wg.Done()

	pending := make(map[string]diagnosticUpdate)
	due := make(map[string]time.Time)
	latest := make(map[string]uint64)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			p.drainPending(pending, latest)
			return
		case update := <-p.updates:
			p.accept(update, pending, due, latest)
		case now := <-ticker.C:
			p.flushDue(now, pending, due, latest)
		}
	}
}

func (p *diagnosticsPublisher) accept(update diagnosticUpdate, pending map[string]diagnosticUpdate, due map[string]time.Time, latest map[string]uint64) {
	if update.Generation < latest[update.URI] {
		return
	}
	if update.Debounce {
		if existing, ok := pending[update.URI]; ok && update.Generation < existing.Generation {
			return
		}
		pending[update.URI] = update
		due[update.URI] = time.Now().Add(diagnosticDebounce)
		return
	}
	p.send(update)
	latest[update.URI] = update.Generation
	time.Sleep(diagnosticRate)
}

func (p *diagnosticsPublisher) flushDue(now time.Time, pending map[string]diagnosticUpdate, due map[string]time.Time, latest map[string]uint64) {
	uris := make([]string, 0, len(pending))
	for uri, at := range due {
		if !now.Before(at) {
			uris = append(uris, uri)
		}
	}
	sort.Strings(uris)
	for _, uri := range uris {
		update := pending[uri]
		delete(pending, uri)
		delete(due, uri)
		if update.Generation < latest[uri] {
			continue
		}
		p.send(update)
		latest[uri] = update.Generation
		time.Sleep(diagnosticRate)
	}
}

func (p *diagnosticsPublisher) drainPending(pending map[string]diagnosticUpdate, latest map[string]uint64) {
	for {
		select {
		case update := <-p.updates:
			if existing, ok := pending[update.URI]; ok && update.Generation < existing.Generation {
				continue
			}
			if update.Generation >= latest[update.URI] {
				pending[update.URI] = update
			}
		default:
			uris := make([]string, 0, len(pending))
			for uri := range pending {
				uris = append(uris, uri)
			}
			sort.Strings(uris)
			for _, uri := range uris {
				update := pending[uri]
				if update.Generation >= latest[uri] {
					p.send(update)
					latest[uri] = update.Generation
				}
			}
			return
		}
	}
}

func (p *diagnosticsPublisher) send(update diagnosticUpdate) {
	p.mu.RLock()
	notifier := p.notifier
	p.mu.RUnlock()
	if notifier == nil {
		return
	}

	_ = notifier.Notify(context.Background(), "textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         update.URI,
		Diagnostics: toProtocolDiagnostics(update.Diagnostics),
	})
}
