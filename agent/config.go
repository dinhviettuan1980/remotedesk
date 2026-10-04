package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type Config struct {
	Server  string `json:"server"`            // e.g. https://remote.tuandv.id.vn
	Token   string `json:"token"`             // shared secret, same as relay REMOTE_TOKEN
	ID      string `json:"id,omitempty"`      // stable machine id; defaults to hostname
	Name    string `json:"name,omitempty"`    // display name; defaults to hostname
	FFmpeg  string `json:"ffmpeg,omitempty"`  // path to ffmpeg; auto-detected when empty
	Encoder string `json:"encoder,omitempty"` // "auto" or an ffmpeg encoder name
	Capture string `json:"capture,omitempty"` // windows: "auto", "ddagrab" or "gdigrab"
	Display int    `json:"display,omitempty"` // 0 = primary display
}

func loadConfig(explicit string) (*Config, string, error) {
	var candidates []string
	if explicit != "" {
		candidates = []string{explicit}
	} else {
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.json"))
		}
		if dir, err := os.UserConfigDir(); err == nil {
			candidates = append(candidates, filepath.Join(dir, "RemoteDesk", "config.json"))
		}
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, p, err
		}
		// PowerShell 5's Set-Content -Encoding UTF8 writes a BOM.
		data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
		var c Config
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, p, fmt.Errorf("%s: %w", p, err)
		}
		if c.Server == "" || c.Token == "" {
			return nil, p, fmt.Errorf("%s: server and token are required", p)
		}
		c.Server = strings.TrimRight(c.Server, "/")
		host, _ := os.Hostname()
		host = strings.TrimSuffix(host, ".local")
		if c.Name == "" {
			c.Name = host
		}
		if c.ID == "" {
			c.ID = slug(host)
		}
		if c.Encoder == "" {
			c.Encoder = "auto"
		}
		if c.Capture == "" {
			c.Capture = "auto"
		}
		return &c, p, nil
	}
	return nil, "", fmt.Errorf("no config.json found (looked in %s)", strings.Join(candidates, ", "))
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		s = runtime.GOOS
	}
	return s
}

// findFFmpeg: config → next to the binary/config → PATH → common install dirs.
func findFFmpeg(c *Config, cfgDir string) (string, error) {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	var cands []string
	if c.FFmpeg != "" {
		cands = append(cands, c.FFmpeg)
	}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), name))
	}
	cands = append(cands, filepath.Join(cfgDir, name))
	if p, err := exec.LookPath(name); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg")
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("ffmpeg not found (set \"ffmpeg\" in config.json)")
}
