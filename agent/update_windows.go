package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// selfUpdate downloads this account's current installer and runs it silently.
// The agent already runs elevated (task with highest privileges), so there is
// no UAC prompt — which a remote viewer could not answer anyway (secure desktop).
// The installer is started as a one-shot scheduled task rather than as a child,
// so it survives the installer stopping this agent.
func selfUpdate(cfg *Config) error {
	req, err := http.NewRequest("GET", cfg.Server+"/dl/RemoteDesk-Agent-Setup.exe", nil)
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
		return fmt.Errorf("tải bộ cài: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return err
	}
	// Must be the NSIS installer carrying our config trailer, not an error page.
	if len(data) < 1<<20 || !bytes.Equal(data[len(data)-10:len(data)-4], []byte("RDCFG1")) {
		return errors.New("bộ cài tải về không hợp lệ")
	}
	path := filepath.Join(os.TempDir(), "RemoteDesk-Update.exe")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.Command("schtasks", args...)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("schtasks %s: %v %s", args[0], err, firstLine(string(out)))
		}
		return nil
	}
	if err := run("/Create", "/TN", "RemoteDesk-Update", "/TR", `"`+path+`" /S`, "/SC", "ONCE", "/ST", "00:00", "/RL", "HIGHEST", "/F"); err != nil {
		return err
	}
	return run("/Run", "/TN", "RemoteDesk-Update")
}
