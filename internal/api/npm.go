package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// npmCache looks up dist-tags.latest for packages with a 30 minute TTL.
type npmCache struct {
	mu    sync.Mutex
	items map[string]npmItem
}

type npmItem struct {
	version string
	at      time.Time
}

func newNpmCache() *npmCache { return &npmCache{items: map[string]npmItem{}} }

func (c *npmCache) get(ctx context.Context, pkg string) string {
	c.mu.Lock()
	it, ok := c.items[pkg]
	c.mu.Unlock()
	if ok && time.Since(it.at) < 30*time.Minute {
		return it.version
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://registry.npmjs.org/"+pkg+"/latest", nil)
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
	c.mu.Lock()
	c.items[pkg] = npmItem{version: doc.Version, at: time.Now()}
	c.mu.Unlock()
	return doc.Version
}
