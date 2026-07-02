package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
)

const (
	// optionsChannelFallback is the channel used whenever the locked nixpkgs input
	// does not name a NixOS release channel (missing lock, no nixpkgs, or a git
	// branch such as "master").
	optionsChannelFallback = "nixos-unstable"
	// optionsCacheTTL is how long a cached dataset is served without re-downloading.
	optionsCacheTTL = 7 * 24 * time.Hour
	// optionsDownloadTimeout bounds the single download attempt.
	optionsDownloadTimeout = 60 * time.Second
)

// optionsChannelPattern matches a NixOS release channel ref such as "nixos-25.05"
// or "nixos-unstable"; anything else falls back to the unstable channel.
var optionsChannelPattern = regexp.MustCompile(`^nixos-[a-z0-9.]+$`)

// initializeOptionsParams decodes the dataset-hover settings from the initialize
// params' initializationOptions: the option-hover path and the package-hover path.
type initializeOptionsParams struct {
	InitializationOptions struct {
		OptionsPath  string `json:"optionsPath"`
		PackagesPath string `json:"packagesPath"`
	} `json:"initializationOptions"`
}

// initializeOptionsPath extracts initializationOptions.optionsPath from the
// initialize params, defaulting to "" (auto mode) when absent or malformed.
func initializeOptionsPath(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var decoded initializeOptionsParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return ""
	}
	return decoded.InitializationOptions.OptionsPath
}

// EnableOptionsDownload turns on auto-download of both auto-loaded datasets (the
// NixOS options and the channel packages) for their default (empty path) modes.
// The name predates the packages dataset; the single switch gates both. The real
// server enables it; tests leave it off so no test performs network I/O.
// Explicit-path and "off" modes are unaffected.
func (h *Handler) EnableOptionsDownload() {
	h.mu.Lock()
	h.optionsDownloadEnabled = true
	h.mu.Unlock()
}

// optionsSnapshot returns the currently published options index, or nil when the
// dataset has not loaded (or the feature is disabled).
func (h *Handler) optionsSnapshot() *options.Index {
	return h.optionsIndex.Load()
}

// startOptionsLoad reacts to the initialize optionsPath setting exactly once.
// "off" disables the feature; an explicit path loads that decompressed
// options.json synchronously (a local read, so hover is ready the moment
// initialize returns); an empty path selects auto mode, which downloads and
// caches the dataset for the locked nixpkgs channel on a background goroutine
// after workspace discovery. Auto mode's network fetch runs only when the process
// enabled it (EnableOptionsDownload); tests leave it off.
func (h *Handler) startOptionsLoad(params json.RawMessage) {
	optionsPath := initializeOptionsPath(params)
	h.optionsOnce.Do(func() {
		switch {
		case optionsPath == "off":
			return
		case optionsPath != "":
			h.loadOptionsFromFile(optionsPath)
		default:
			h.startOptionsAutoLoad()
		}
	})
}

// loadOptionsFromFile parses a decompressed options.json at path and publishes
// the resulting index. Any failure logs a single line and leaves the index unset,
// so hover degrades to null rather than an error.
func (h *Handler) loadOptionsFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		logOptions("read %s: %v", path, err)
		return
	}
	ix, err := options.Parse(data)
	if err != nil {
		logOptions("parse %s: %v", path, err)
		return
	}
	h.optionsIndex.Store(ix)
}

// startOptionsAutoLoad kicks off the auto-mode load on a background goroutine that
// first waits for workspace discovery so the flake lock is available for channel
// selection. It is a no-op when auto-download is not enabled, so no test reaches
// the network.
func (h *Handler) startOptionsAutoLoad() {
	h.mu.RLock()
	enabled := h.optionsDownloadEnabled
	done := h.workspaceDone
	h.mu.RUnlock()
	if !enabled {
		return
	}
	ctx := h.optionsCtx
	go func() {
		if done != nil {
			select {
			case <-ctx.Done():
				return
			case <-done:
			}
		}
		h.loadOptionsAuto(ctx)
	}()
}

// loadOptionsAuto selects the channel from the flake lock, then serves the
// dataset from a fresh cache, a download, or (if the download fails) a stale
// cache, publishing the index on success. Every failure path logs one line and
// leaves the index unset.
func (h *Handler) loadOptionsAuto(ctx context.Context) {
	channel := h.optionsChannelFromLock(ctx)
	// Record the channel so option hover can link declaration paths to their
	// nixpkgs source. Only auto mode sets this; explicit-path loads leave it empty
	// and keep declarations backticked.
	h.setOptionsChannel(channel)

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		logOptions("cache dir: %v", err)
		return
	}
	cachePath := filepath.Join(cacheDir, "nixls", "options", channel+".json")

	if info, err := os.Stat(cachePath); err == nil && optionsCacheFresh(info.ModTime(), time.Now()) {
		if h.publishOptionsFromCache(cachePath) {
			return
		}
	}

	data, err := downloadOptions(ctx, channel)
	if err != nil {
		logOptions("download %s: %v", channel, err)
		// A stale cache is still better than nothing when the download fails.
		h.publishOptionsFromCache(cachePath)
		return
	}
	if err := writeOptionsCache(cachePath, data); err != nil {
		logOptions("cache write %s: %v", cachePath, err)
	}
	ix, err := options.Parse(data)
	if err != nil {
		logOptions("parse download %s: %v", channel, err)
		return
	}
	h.optionsIndex.Store(ix)
}

// publishOptionsFromCache parses the cache file at path and publishes the index,
// reporting whether it succeeded.
func (h *Handler) publishOptionsFromCache(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	ix, err := options.Parse(data)
	if err != nil {
		return false
	}
	h.optionsIndex.Store(ix)
	return true
}

// optionsChannelFromLock reads the current flake lock and selects the NixOS
// channel for the options dataset. It returns the fallback on any error.
func (h *Handler) optionsChannelFromLock(ctx context.Context) string {
	lock, hasLock, err := facts.FlakeLock(ctx, h.memo)
	if err != nil {
		return optionsChannelFallback
	}
	return optionsChannel(lock, hasLock)
}

// optionsChannel selects the NixOS channel from the flake lock: the root nixpkgs
// input's original ref when it names a release channel (e.g. "nixos-25.05"),
// otherwise the unstable fallback (missing lock, no nixpkgs, or a git branch such
// as "master").
func optionsChannel(lock *flake.Lock, hasLock bool) string {
	if !hasLock || lock == nil {
		return optionsChannelFallback
	}
	ref, ok := lock.RootInputs()["nixpkgs"]
	if !ok || ref.Key == "" {
		return optionsChannelFallback
	}
	node, ok := lock.Nodes[ref.Key]
	if !ok || node.Original == nil {
		return optionsChannelFallback
	}
	if optionsChannelPattern.MatchString(node.Original.Ref) {
		return node.Original.Ref
	}
	return optionsChannelFallback
}

// optionsCacheFresh reports whether a cache file last modified at modTime is still
// within the options TTL window at now. It is a thin wrapper over the shared
// cacheFresh so the options and packages loaders share one freshness rule.
func optionsCacheFresh(modTime, now time.Time) bool {
	return cacheFresh(modTime, now, optionsCacheTTL)
}

// downloadOptions fetches and brotli-decompresses the options.json artifact for
// channel, returning the whole decompressed JSON. It is a single attempt bounded
// by ctx and the options timeout. It is never exercised in tests (no test
// performs network I/O).
func downloadOptions(ctx context.Context, channel string) ([]byte, error) {
	url := "https://channels.nixos.org/" + channel + "/options.json.br"
	r, cleanup, err := fetchBrotli(ctx, url, optionsDownloadTimeout)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return io.ReadAll(r)
}

// writeOptionsCache writes the decompressed options dataset to path atomically via
// the shared cache writer.
func writeOptionsCache(path string, data []byte) error {
	return writeCacheFileAtomic(path, data)
}

// logOptions writes a single diagnostic line to stderr for an options-loading
// failure. Loading never surfaces an error to the client; hover simply stays null.
func logOptions(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "nixls: options: "+format+"\n", args...)
}
