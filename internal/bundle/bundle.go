// Package bundle handles skill directories as content-addressed file sets.
// The digest is computed over the sorted (path, mode-bit, content) tuples so
// that the same logical directory yields the same digest on server and client.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type File struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"` // permission bits only
	Content []byte `json:"content,omitempty"`
}

type Set struct {
	Files []File
}

var skipNames = map[string]bool{".DS_Store": true, ".git": true, "node_modules": true, "__pycache__": true}

// ReadDir loads a directory into a Set, following no symlinks.
func ReadDir(root string) (*Set, error) {
	s := &Set{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipNames[d.Name()] && p != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks, sockets, etc.
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s.Files = append(s.Files, File{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode().Perm()), Content: b})
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Normalize()
	return s, nil
}

func (s *Set) Normalize() {
	for i := range s.Files {
		s.Files[i].Path = strings.TrimPrefix(filepath.ToSlash(s.Files[i].Path), "./")
		// Only keep the executable bit as a distinguishing feature.
		if s.Files[i].Mode&0o111 != 0 {
			s.Files[i].Mode = 0o755
		} else {
			s.Files[i].Mode = 0o644
		}
	}
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
}

func (s *Set) Size() int64 {
	var n int64
	for _, f := range s.Files {
		n += int64(len(f.Content))
	}
	return n
}

// Digest is stable across machines: sha256 over "path\0mode\0len\0content" records.
func (s *Set) Digest() string {
	s.Normalize()
	h := sha256.New()
	for _, f := range s.Files {
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", f.Path, f.Mode, len(f.Content))
		h.Write(f.Content)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (s *Set) Pack() ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range s.Files {
		hdr := &tar.Header{Name: f.Path, Mode: int64(f.Mode), Size: int64(len(f.Content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Unpack(r io.Reader) (*Set, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	s := &Set{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		if clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		s.Files = append(s.Files, File{Path: clean, Mode: uint32(hdr.Mode), Content: b})
	}
	s.Normalize()
	return s, nil
}

// WriteDir materialises the set into dir, replacing any existing content.
// It writes into a sibling temp dir first and then swaps, so a failed write
// never leaves a half-updated skill behind.
func (s *Set) WriteDir(dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	for _, f := range s.Files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if !strings.HasPrefix(dst, tmp+string(os.PathSeparator)) {
			cleanup()
			return fmt.Errorf("unsafe path %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			cleanup()
			return err
		}
		if err := os.WriteFile(dst, f.Content, os.FileMode(f.Mode)); err != nil {
			cleanup()
			return err
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			cleanup()
			return err
		}
	}
	return os.Rename(tmp, dir)
}

// ValidateSkill checks the minimum contract: a SKILL.md at the root.
func (s *Set) ValidateSkill() error {
	for _, f := range s.Files {
		if f.Path == "SKILL.md" {
			return nil
		}
	}
	return fmt.Errorf("bundle has no SKILL.md at its root")
}

// Description extracts the `description:` line from SKILL.md frontmatter, if present.
func (s *Set) Description() string {
	for _, f := range s.Files {
		if f.Path != "SKILL.md" {
			continue
		}
		lines := strings.Split(string(f.Content), "\n")
		if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
			return ""
		}
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) == "---" {
				break
			}
			if strings.HasPrefix(l, "description:") {
				return strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "description:")), `"'`)
			}
		}
	}
	return ""
}
