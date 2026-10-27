package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
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
	Notify  bool   `json:"notify,omitempty"`  // show a notice on this machine when someone starts watching
	// AutoUpdate: install newer agents from the relay while nobody watches (nil = on).
	AutoUpdate      *bool  `json:"autoUpdate,omitempty"`
	AutoUpdateTried string `json:"autoUpdateTried,omitempty"` // version already tried (autoupdate.go)
}

// saveConfigValue sets one key in config.json, keeping everything else (and
// the file's permissions: on Windows its ACL protects the token).
func saveConfigValue(path, key string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), &cfg); err != nil {
		return err
	}
	cfg[key] = value
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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

type installerConfig struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func readInstallerConfig(src, tokenFile string) (installerConfig, error) {
	var in installerConfig
	data, err := os.ReadFile(src)
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), &in); err != nil {
		return in, err
	}
	if in.Server == "" {
		return in, errors.New("installer config: server is required")
	}
	if tokenFile != "" {
		if b, err := os.ReadFile(tokenFile); err == nil {
			if t := strings.TrimSpace(string(bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF")))); t != "" {
				in.Token = t
			}
		}
	}
	return in, nil
}

// checkToken asks the relay whether the code typed during setup is valid.
func checkToken(src, tokenFile string) int {
	in, err := readInstallerConfig(src, tokenFile)
	if err != nil || in.Token == "" {
		return 3
	}
	req, err := http.NewRequest("GET", strings.TrimRight(in.Server, "/")+"/api/token-check", nil)
	if err != nil {
		return 4
	}
	req.Header.Set("Authorization", "Bearer "+in.Token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 4
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return 0
	case 401:
		return 3
	}
	return 4
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

// importConfig is run by the Windows installer: it merges server + token from
// the installer's appended config into config.json next to the binary,
// keeping this machine's id, name and tuning, and points at the bundled ffmpeg.
//
// The shared installer carries only the server: the access code comes from
// tokenFile (typed during setup), and an empty one keeps the current code.
func importConfig(src, tokenFile string) error {
	in, err := readInstallerConfig(src, tokenFile)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	dst := filepath.Join(dir, "config.json")
	cfg := map[string]any{}
	if old, err := os.ReadFile(dst); err == nil {
		_ = json.Unmarshal(bytes.TrimPrefix(old, []byte("\xEF\xBB\xBF")), &cfg) // keep id, name, encoder…
	}
	if in.Token == "" {
		if old, _ := cfg["token"].(string); old != "" {
			in.Token = old
		} else {
			return errors.New("chưa có mã truy cập")
		}
	}
	cfg["server"] = strings.TrimRight(in.Server, "/")
	cfg["token"] = in.Token
	ff := filepath.Join(dir, "ffmpeg")
	if runtime.GOOS == "windows" {
		ff += ".exe"
	}
	if _, err := os.Stat(ff); err == nil {
		cfg["ffmpeg"] = ff
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o600)
}
