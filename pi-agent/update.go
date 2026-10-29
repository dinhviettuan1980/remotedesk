package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// distName: the relay's file for this build ("pi-agent-linux-arm64", …).
func distName() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv6" // GOARM=6: Pi Zero / 1, also runs on 32-bit Pi OS on newer boards
	}
	return "pi-agent-" + runtime.GOOS + "-" + arch
}

// selfUpdate downloads the current build from the relay, checks that it runs,
// swaps it in and exits; systemd (Restart=always) starts the new one.
func selfUpdate(cfg *Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", cfg.Server+"/dl/"+distName(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("tải %s: HTTP %d", distName(), resp.StatusCode)
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	out, err := exec.Command(tmp, "-version").Output()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("bản mới không chạy được: %v", err)
	}
	newVer := strings.TrimSpace(string(out))
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	log.Printf("updated %s → %s, restarting", version, newVer)
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
	return nil
}
