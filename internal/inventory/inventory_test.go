package inventory

import (
	"context"
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

	got := shadowedCopies(context.Background(), "foo", active)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct shadowed copies, got %d: %v", len(got), got)
	}
	if got[0].Path != filepath.Join(other, "foo") || got[1].Path != filepath.Join(usrBin, "foo") {
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

	if got := shadowedCopies(context.Background(), "foo", active); len(got) != 0 {
		t.Fatalf("want no shadowed copies, got %v", got)
	}
}

// A hidden copy is only worth flagging if you know what it is: the console
// tells "an old install is hijacking PATH" apart from "another app ships its
// own copy" by comparing versions, so we have to collect them.
func TestShadowedCopiesReportsVersions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixtures are a unix thing")
	}
	root := t.TempDir()
	newer := filepath.Join(root, "newer")
	mute := filepath.Join(root, "mute")
	for _, d := range []string{newer, mute} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(newer, "foo"), []byte("#!/bin/sh\necho 'foo-cli 2.5.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exits non-zero and says nothing, like a wrapper that wants a terminal.
	if err := os.WriteFile(filepath.Join(mute, "foo"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	activeDir := filepath.Join(root, "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(activeDir, "foo")
	if err := os.WriteFile(active, []byte("#!/bin/sh\necho 'foo-cli 1.0.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	sep := string(os.PathListSeparator)
	t.Setenv("PATH", activeDir+sep+newer+sep+mute)

	got := shadowedCopies(context.Background(), "foo", active)
	if len(got) != 2 {
		t.Fatalf("want 2 copies, got %d: %v", len(got), got)
	}
	if got[0].Version != "2.5.0" {
		t.Errorf("want version 2.5.0 for the readable copy, got %q", got[0].Version)
	}
	// Unknown is fine; the UI just won't claim it's stale.
	if got[1].Version != "" {
		t.Errorf("want empty version for the silent copy, got %q", got[1].Version)
	}
}
