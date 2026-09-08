// Package secret is AES-256-GCM sealing for values at rest. The master key
// lives on the server only (env AGENTDECK_MASTER_KEY or <data>/master_key).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const prefix = "enc:v1:"

type Box struct{ aead cipher.AEAD }

// Load reads the key from env or file, generating one if neither exists.
func Load(path string) (*Box, error) {
	var raw []byte
	if v := strings.TrimSpace(os.Getenv("AGENTDECK_MASTER_KEY")); v != "" {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 32 {
			return nil, errors.New("AGENTDECK_MASTER_KEY must be 64 hex chars")
		}
		raw = b
	} else if b, err := os.ReadFile(path); err == nil {
		raw, err = hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("bad master key file %s", path)
		}
	} else {
		raw = make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "generated master key -> %s (back this up; without it secrets are unrecoverable)\n", path)
	}
	blk, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain string) string {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	ct := b.aead.Seal(nil, nonce, []byte(plain), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(append(nonce, ct...))
}

func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

func (b *Box) Open(s string) (string, error) {
	if !IsSealed(s) {
		return s, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("ciphertext too short")
	}
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
