package bundle

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndDigest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: hi there\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644)

	s, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(s.Files))
	}
	if s.Description() != "hi there" {
		t.Fatalf("description = %q", s.Description())
	}
	d1 := s.Digest()
	pk, _ := s.Pack()
	s2, err := Unpack(bytes.NewReader(pk))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Digest() != d1 {
		t.Fatalf("digest changed across pack/unpack: %s vs %s", d1, s2.Digest())
	}
	out := filepath.Join(t.TempDir(), "x")
	if err := s2.WriteDir(out); err != nil {
		t.Fatal(err)
	}
	s3, _ := ReadDir(out)
	if s3.Digest() != d1 {
		t.Fatalf("digest changed after WriteDir")
	}
	fi, _ := os.Stat(filepath.Join(out, "scripts", "run.sh"))
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exec bit lost")
	}
}

func TestUnpackRejectsTraversal(t *testing.T) {
	s := &Set{Files: []File{{Path: "../evil", Mode: 0o644, Content: []byte("x")}}}
	pk, _ := s.Pack()
	if _, err := Unpack(bytes.NewReader(pk)); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
