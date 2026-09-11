package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testCache(base string) *npmCache {
	c := newNpmCache()
	c.base = base
	return c
}

// Internal-registry packages (bytedcli) are never on npmjs.org. If a miss isn't
// cached, every console page load re-requests them and waits out the timeout.
func TestNpmCacheRemembersMisses(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := testCache(srv.URL + "/")

	for i := 0; i < 3; i++ {
		if v := c.get(context.Background(), "@bytedance-dev/bytedcli"); v != "" {
			t.Fatalf("want empty version for unknown package, got %q", v)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("want 1 registry request, got %d", got)
	}
}

func TestNpmCacheServesHitFromMemory(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"version":"1.0.95"}`))
	}))
	defer srv.Close()
	c := testCache(srv.URL + "/")

	for i := 0; i < 3; i++ {
		if v := c.get(context.Background(), "@larksuite/cli"); v != "1.0.95" {
			t.Fatalf("want 1.0.95, got %q", v)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("want 1 registry request, got %d", got)
	}
}

// getMany is what the handler calls; a package the registry doesn't know must
// still appear in the map so the UI can render "no latest" instead of failing.
func TestNpmCacheGetManyIncludesUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/@larksuite/cli/latest" {
			_, _ = w.Write([]byte(`{"version":"1.0.95"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := testCache(srv.URL + "/")

	out := c.getMany(context.Background(), []string{"@larksuite/cli", "@bytedance-dev/bytedcli"})
	if out["@larksuite/cli"] != "1.0.95" {
		t.Fatalf("want 1.0.95 for lark-cli, got %q", out["@larksuite/cli"])
	}
	if v, ok := out["@bytedance-dev/bytedcli"]; !ok || v != "" {
		t.Fatalf("want empty entry for internal package, got %q ok=%v", v, ok)
	}
}
