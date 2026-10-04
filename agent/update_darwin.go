package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// selfUpdate fetches this account's install.sh and runs it: it replaces the
// signed RemoteDesk.app and restarts the LaunchAgent. Grants survive because
// the new build carries the same bundle id and certificate. The script runs
// in its own session so launchd stopping this agent doesn't take it down.
func selfUpdate(cfg *Config) error {
	req, err := http.NewRequest("GET", cfg.Server+"/install.sh?app=0", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("tải bộ cài: HTTP %d", resp.StatusCode)
	}
	script, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(script), "#!/bin/bash") {
		return errors.New("bộ cài tải về không hợp lệ")
	}
	path := filepath.Join(os.TempDir(), "remotedesk-update.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(os.TempDir(), "remotedesk-update.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command("/bin/bash", path)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	logf.Close()
	return cmd.Process.Release()
}
