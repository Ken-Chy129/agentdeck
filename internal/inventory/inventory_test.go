package inventory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A merged-/usr distro has /bin as a symlink to /usr/bin, and $PATH lists both.
// Those two entries are one binary and must not be reported as two stale copies.
func TestShadowedCopiesDedupesPathAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink-based PATH aliasing is a unix layout")
	}
	root := t.TempDir()
	usrBin := filepath.Join(root, "usr", "bin")
	other := filepath.Join(root, "other")
	for _, d := range []string{usrBin, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(usrBin, "foo"))
	write(filepath.Join(other, "foo"))
	// /bin -> usr/bin
	if err := os.Symlink(filepath.Join("usr", "bin"), filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}

	active := filepath.Join(root, "active", "foo")
	if err := os.MkdirAll(filepath.Dir(active), 0o755); err != nil {
		t.Fatal(err)
	}
	write(active)

	t.Setenv("PATH", other+string(os.PathListSeparator)+usrBin+string(os.PathListSeparator)+filepath.Join(root, "bin"))

	got := shadowedCopies("foo", active)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct shadowed copies, got %d: %v", len(got), got)
	}
	if got[0] != filepath.Join(other, "foo") || got[1] != filepath.Join(usrBin, "foo") {
		t.Fatalf("unexpected copies: %v", got)
	}
}

// The binary that actually runs is not "shadowed" by an alias of itself.
func TestShadowedCopiesSkipsActiveAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink-based PATH aliasing is a unix layout")
	}
	root := t.TempDir()
	usrBin := filepath.Join(root, "usr", "bin")
	if err := os.MkdirAll(usrBin, 0o755); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(usrBin, "foo")
	if err := os.WriteFile(active, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("usr", "bin"), filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", usrBin+string(os.PathListSeparator)+filepath.Join(root, "bin"))

	if got := shadowedCopies("foo", active); len(got) != 0 {
		t.Fatalf("want no shadowed copies, got %v", got)
	}
}
