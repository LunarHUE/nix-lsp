package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
)

const (
	// packagesCacheTTL is how long a cached packages dataset is served without
	// re-downloading.
	packagesCacheTTL = 7 * 24 * time.Hour
	// packagesDownloadTimeout bounds the single download attempt. It is larger than
	// the options timeout because the packages artifact is an order of magnitude
	// bigger (~10 MB compressed, hundreds of MB decompressed).
	packagesDownloadTimeout = 120 * time.Second
)

// initializePackagesPath extracts initializationOptions.packagesPath from the
// initialize params, defaulting to "" (auto mode) when absent or malformed.
func initializePackagesPath(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var decoded initializeOptionsParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return ""
	}
	return decoded.InitializationOptions.PackagesPath
}

// packagesSnapshot returns the currently published packages index, or nil when
// the dataset has not loaded (or the feature is disabled).
func (h *Handler) packagesSnapshot() *packages.Index {
	return h.packagesIndex.Load()
}

// startPackagesLoad reacts to the initialize packagesPath setting exactly once,
// mirroring startOptionsLoad. "off" disables the feature; an explicit path loads
// that RAW-shape packages.json synchronously (a local read via ParseStream, so
// hover is ready the moment initialize returns); an empty path selects auto mode,
// which downloads and caches the dataset on a background goroutine after workspace
// discovery. Auto mode's network fetch runs only when the process enabled it via
// EnableOptionsDownload; tests leave it off.
func (h *Handler) startPackagesLoad(params json.RawMessage) {
	packagesPath := initializePackagesPath(params)
	h.packagesOnce.Do(func() {
		switch {
		case packagesPath == "off":
			return
		case packagesPath != "":
			h.loadPackagesFromFile(packagesPath)
		default:
			h.startPackagesAutoLoad()
		}
	})
}

// loadPackagesFromFile streams a RAW-shape packages.json at path and publishes the
// resulting index. Any failure logs a single line and leaves the index unset, so
// hover degrades to null rather than an error.
func (h *Handler) loadPackagesFromFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		logPackages("open %s: %v", path, err)
		return
	}
	defer f.Close()
	ix, err := packages.ParseStream(f)
	if err != nil {
		logPackages("parse %s: %v", path, err)
		return
	}
	h.packagesIndex.Store(ix)
}

// startPackagesAutoLoad kicks off the auto-mode load on a background goroutine that
// first waits for workspace discovery so the flake lock is available for channel
// selection. It is a no-op when auto-download is not enabled, so no test reaches
// the network.
func (h *Handler) startPackagesAutoLoad() {
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
		h.loadPackagesAuto(ctx)
	}()
}

// loadPackagesAuto selects the channel from the flake lock, then serves the
// packages dataset from a fresh trimmed cache, a streaming download, or (if the
// download fails) a stale trimmed cache, publishing the index on success. Every
// failure path logs one line and leaves the index unset.
func (h *Handler) loadPackagesAuto(ctx context.Context) {
	channel := h.optionsChannelFromLock(ctx)

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		logPackages("cache dir: %v", err)
		return
	}
	cachePath := filepath.Join(cacheDir, "nixls", "packages", channel+".json")

	if info, err := os.Stat(cachePath); err == nil && cacheFresh(info.ModTime(), time.Now(), packagesCacheTTL) {
		if h.publishPackagesFromCache(cachePath) {
			return
		}
	}

	ix, err := h.downloadPackagesIndex(ctx, channel)
	if err != nil {
		logPackages("download %s: %v", channel, err)
		// A stale cache is still better than nothing when the download fails.
		h.publishPackagesFromCache(cachePath)
		return
	}
	// Cache the trimmed form (tens of MB), never the raw artifact.
	if trimmed, err := ix.MarshalTrimmed(); err != nil {
		logPackages("marshal trimmed %s: %v", channel, err)
	} else if err := writeCacheFileAtomic(cachePath, trimmed); err != nil {
		logPackages("cache write %s: %v", cachePath, err)
	}
	h.packagesIndex.Store(ix)
}

// downloadPackagesIndex streams the packages.json.br artifact for channel directly
// into an Index: ParseStream consumes the brotli reader as a token stream, so the
// hundreds-of-MB decompressed artifact is never buffered in memory. It is never
// exercised in tests (no test performs network I/O).
func (h *Handler) downloadPackagesIndex(ctx context.Context, channel string) (*packages.Index, error) {
	url := "https://channels.nixos.org/" + channel + "/packages.json.br"
	r, cleanup, err := fetchBrotli(ctx, url, packagesDownloadTimeout)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return packages.ParseStream(r)
}

// publishPackagesFromCache parses the trimmed cache file at path and publishes the
// index, reporting whether it succeeded.
func (h *Handler) publishPackagesFromCache(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	ix, err := packages.ParseTrimmed(data)
	if err != nil {
		return false
	}
	h.packagesIndex.Store(ix)
	return true
}

// logPackages writes a single diagnostic line to stderr for a packages-loading
// failure. Loading never surfaces an error to the client; hover simply stays null.
func logPackages(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "nixls: packages: "+format+"\n", args...)
}
