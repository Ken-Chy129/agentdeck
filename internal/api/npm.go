package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// npmCache looks up dist-tags.latest for packages. Entries are fresh for 30
// minutes; after that the stale value is returned immediately and refreshed in
// the background, so the console never blocks on the registry once warmed.
type npmCache struct {
	mu       sync.Mutex
	items    map[string]npmItem
	inflight map[string]bool
	base     string
}

type npmItem struct {
	version string
	at      time.Time
}

const npmFresh = 30 * time.Minute

const npmRegistry = "https://registry.npmjs.org/"

func newNpmCache() *npmCache {
	return &npmCache{items: map[string]npmItem{}, inflight: map[string]bool{}, base: npmRegistry}
}

// getMany resolves all packages concurrently.
func (c *npmCache) getMany(ctx context.Context, pkgs []string) map[string]string {
	out := make(map[string]string, len(pkgs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range pkgs {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			v := c.get(ctx, p)
			mu.Lock()
			out[p] = v
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

func (c *npmCache) get(ctx context.Context, pkg string) string {
	c.mu.Lock()
	it, ok := c.items[pkg]
	c.mu.Unlock()
	if ok && time.Since(it.at) < npmFresh {
		return it.version
	}
	if ok { // stale: serve it, refresh in background
		c.refreshAsync(pkg)
		return it.version
	}
	return c.fetch(ctx, pkg)
}

func (c *npmCache) refreshAsync(pkg string) {
	c.mu.Lock()
	if c.inflight[pkg] {
		c.mu.Unlock()
		return
	}
	c.inflight[pkg] = true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c.fetch(ctx, pkg)
		c.mu.Lock()
		delete(c.inflight, pkg)
		c.mu.Unlock()
	}()
}

func (c *npmCache) fetch(ctx context.Context, pkg string) string {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+pkg+"/latest", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var doc struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	// Cache the miss too. Internal-registry packages (bytedcli) will never be
	// on npmjs.org, and without this every page load pays the lookup again.
	// An empty version simply means "no latest to compare against".
	c.mu.Lock()
	c.items[pkg] = npmItem{version: doc.Version, at: time.Now()}
	c.mu.Unlock()
	return doc.Version
}
