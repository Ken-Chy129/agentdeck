// Package sync implements the machine-side logic: config, lockfile, HTTP
// client, and the reconcile loop that makes local skill dirs match the server.
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/apply"
	"github.com/Ken-Chy129/agentdeck/internal/protocol"
)

const Version = "0.4.0"

// Config lives at ~/.config/agentdeck/config.json (0600).
type Config struct {
	Server       string   `json:"server"`
	MachineID    string   `json:"machine_id"`
	MachineToken string   `json:"machine_token"`
	Name         string   `json:"name"`
	SkillsDir    string   `json:"skills_dir"`          // canonical location, default ~/.agents/skills
	LinkDirs     []string `json:"link_dirs,omitempty"` // agent dirs that get symlinks, default ~/.claude/skills
}

// Lock records what this machine has applied: ~/.agents/skills/.agentdeck-lock.json.
type Lock struct {
	Skills      map[string]LockEntry `json:"skills"`
	Env         map[string]LockEntry `json:"env"`
	Configs     map[string]LockEntry `json:"configs"`
	ConfigState apply.ConfigState    `json:"config_state"`
}

type LockEntry struct {
	ResourceID int64  `json:"resource_id,omitempty"`
	Version    int    `json:"version"`
	VersionID  int64  `json:"version_id"`
	Digest     string `json:"digest"`
	Path       string `json:"path,omitempty"`
	SyncedAt   string `json:"synced_at"`
}

func home() string { h, _ := os.UserHomeDir(); return h }

func ConfigPath() string {
	if p := os.Getenv("AGENTDECK_CONFIG"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "agentdeck", "config.json")
	}
	return filepath.Join(home(), ".config", "agentdeck", "config.json")
}

func LoadConfig() (*Config, error) {
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("not logged in: run `agentdeck login <server> <enroll-token>` first")
		}
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.SkillsDir == "" {
		c.SkillsDir = filepath.Join(home(), ".agents", "skills")
	}
	if c.LinkDirs == nil {
		c.LinkDirs = []string{filepath.Join(home(), ".claude", "skills")}
	}
}

func (c *Config) Save() error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func (c *Config) LockPath() string { return filepath.Join(c.SkillsDir, ".agentdeck-lock.json") }

func (c *Config) LoadLock() *Lock {
	l := &Lock{}
	if b, err := os.ReadFile(c.LockPath()); err == nil {
		_ = json.Unmarshal(b, l)
	}
	if l.Skills == nil {
		l.Skills = map[string]LockEntry{}
	}
	if l.Env == nil {
		l.Env = map[string]LockEntry{}
	}
	if l.Configs == nil {
		l.Configs = map[string]LockEntry{}
	}
	if l.ConfigState.Keys == nil {
		l.ConfigState.Keys = map[string][]string{}
	}
	return l
}

func (c *Config) SaveLock(l *Lock) error {
	if err := os.MkdirAll(c.SkillsDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(l, "", "  ")
	return os.WriteFile(c.LockPath(), append(b, '\n'), 0o644)
}

// ---- HTTP ----

type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, ctype string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("User-Agent", "agentdeck/"+Version)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e struct{ Error string }
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	if out == nil {
		return nil
	}
	if w, ok := out.(io.Writer); ok {
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) JSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	return c.do(ctx, method, path, body, "application/json", out)
}

func (c *Client) Enroll(ctx context.Context, req protocol.EnrollRequest) (*protocol.EnrollResponse, error) {
	var out protocol.EnrollResponse
	err := c.JSON(ctx, "POST", "/api/agent/enroll", req, &out)
	return &out, err
}

func (c *Client) Sync(ctx context.Context, req protocol.SyncRequest) (*protocol.SyncResponse, error) {
	var out protocol.SyncResponse
	err := c.JSON(ctx, "POST", "/api/agent/sync", req, &out)
	return &out, err
}

func (c *Client) Report(ctx context.Context, rep protocol.SyncReport) error {
	return c.JSON(ctx, "POST", "/api/agent/report", rep, nil)
}

// Poll parks a request on the server until a job shows up or the server times
// out. The timeout here must exceed the server's poll window.
func (c *Client) Poll(ctx context.Context) (*protocol.PollResponse, error) {
	var out protocol.PollResponse
	err := c.do(ctx, "GET", "/api/agent/poll", nil, "", &out)
	return &out, err
}

// JobResult reports one finished job right away.
func (c *Client) JobResult(ctx context.Context, res protocol.JobResult) error {
	return c.JSON(ctx, "POST", fmt.Sprintf("/api/agent/jobs/%d/result", res.ID), res, nil)
}

func (c *Client) Archive(ctx context.Context, versionID int64) ([]byte, error) {
	var buf bytes.Buffer
	err := c.do(ctx, "GET", fmt.Sprintf("/api/agent/versions/%d/archive", versionID), nil, "", &buf)
	return buf.Bytes(), err
}

func (c *Client) Publish(ctx context.Context, name string, archive []byte, note string, assignSelf bool) (map[string]any, error) {
	q := "?note=" + urlEscape(note)
	if assignSelf {
		q += "&assign=self"
	}
	var out map[string]any
	err := c.do(ctx, "POST", "/api/agent/skills/"+name+q, bytes.NewReader(archive), "application/gzip", &out)
	return out, err
}

func urlEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "&", "%26", "#", "%23", "?", "%3F", "+", "%2B")
	return r.Replace(s)
}
