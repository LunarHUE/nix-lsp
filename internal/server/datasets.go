package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/andybalholm/brotli"
)

// datasets.go holds the cache and download plumbing shared by the two auto-loaded
// datasets (NixOS options and channel packages). Both artifacts are served from
// channels.nixos.org as brotli-compressed JSON, cached under UserCacheDir with an
// mtime TTL, and written atomically. The options loader reads the whole
// decompressed body; the packages loader streams it, so fetchBrotli hands back a
// reader rather than bytes and each caller decides how to consume it.

// cacheFresh reports whether a cache file last modified at modTime is still within
// ttl at now.
func cacheFresh(modTime, now time.Time, ttl time.Duration) bool {
	return now.Sub(modTime) < ttl
}

// fetchBrotli issues a GET for url, following redirects (the channels host 302s to
// a pinned release artifact), bounded by ctx and timeout, and returns a reader
// that brotli-decompresses the response body. The caller must invoke the returned
// cleanup to close the body and release the timeout. It is never exercised in
// tests (no test performs network I/O).
func fetchBrotli(ctx context.Context, url string, timeout time.Duration) (io.Reader, func(), error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	cleanup := func() {
		resp.Body.Close()
		cancel()
	}
	return brotli.NewReader(resp.Body), cleanup, nil
}

// writeCacheFileAtomic writes data to path atomically: it creates the parent
// directory, writes a sibling temp file, then renames it into place so a reader
// never observes a half-written cache.
func writeCacheFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "dataset-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
