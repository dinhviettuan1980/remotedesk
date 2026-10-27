package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Automatic updates: when the relay serves a newer agent (dist/agent-version.txt,
// written by scripts/build.sh), install it while nobody is watching — the same
// selfUpdate as "Cập nhật agent", so new features reach every machine without
// a click per machine. Each version is tried once: a build that does not take
// (or an installer older than its version file) cannot make the agent loop;
// "Cập nhật agent" still forces it. "autoUpdate": false in config.json turns it off.

const (
	autoUpdateFirst = 10 * time.Minute // after start: let a fresh install settle
	autoUpdateEvery = 6 * time.Hour
)

func (a *Agent) autoUpdateLoop() {
	time.Sleep(autoUpdateFirst)
	for {
		a.maybeAutoUpdate()
		time.Sleep(autoUpdateEvery)
	}
}

func (a *Agent) maybeAutoUpdate() {
	if a.cfg.AutoUpdate != nil && !*a.cfg.AutoUpdate {
		return
	}
	latest, err := fetchLatestVersion(a.cfg)
	if err != nil {
		log.Printf("auto-update: %v", err)
		return
	}
	if !versionNewer(latest, version) || a.cfg.AutoUpdateTried == latest {
		return
	}
	a.mu.Lock()
	watching := a.viewer
	a.mu.Unlock()
	if watching { // never pull the machine away from someone using it
		return
	}
	a.cfg.AutoUpdateTried = latest
	if err := saveConfigValue(a.cfgPath, "autoUpdateTried", latest); err != nil {
		log.Printf("auto-update: save config: %v", err)
		return // without the mark a failing update could repeat every 6 h
	}
	log.Printf("auto-update: %s → %s", version, latest)
	if err := selfUpdate(a.cfg); err != nil {
		log.Printf("auto-update: %v", err)
	}
}

func fetchLatestVersion(cfg *Config) (string, error) {
	req, err := http.NewRequest("GET", cfg.Server+"/dl/agent-version.txt", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("agent-version.txt: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// versionNewer: "0.3.10" > "0.3.9". Anything not dotted numbers is never newer.
func versionNewer(v, than string) bool {
	parse := func(s string) []int {
		var out []int
		for _, p := range strings.Split(s, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(v), parse(than)
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}
