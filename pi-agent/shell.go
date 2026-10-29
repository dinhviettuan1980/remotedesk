package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

// One web terminal: a login shell on a pty, bytes base64-encoded both ways
// (output may split UTF-8 sequences, which JSON strings can't carry).
type shell struct {
	cmd  *exec.Cmd
	pty  *os.File
	done chan struct{}
}

// shellCommand: a login shell as cfg.ShellUser, else the first regular user
// (uid 1000, who has sudo on Raspberry Pi OS), else root. Not root (local
// testing): just the current user's shell.
func shellCommand(cfg *Config) *exec.Cmd {
	if os.Geteuid() != 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return exec.Command(sh, "-l")
	}
	user := cfg.ShellUser
	if user == "" {
		user = firstUser()
	}
	if user == "" || user == "root" {
		return exec.Command("/bin/bash", "-l")
	}
	return exec.Command("su", "-l", user)
}

func firstUser() string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) >= 7 && p[2] == "1000" && !strings.HasSuffix(p[6], "nologin") {
			return p[0]
		}
	}
	return ""
}

func (a *Agent) openShell(vid, cols, rows int) {
	a.closeShell(vid) // one terminal per viewer: reopening starts fresh
	cmd := shellCommand(a.cfg)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	f, err := pty.StartWithSize(cmd, winSize(cols, rows))
	if err != nil {
		a.reply(vid, "sh-exit", map[string]any{"err": err.Error()})
		return
	}
	s := &shell{cmd: cmd, pty: f, done: make(chan struct{})}
	a.mu.Lock()
	a.shells[vid] = s
	a.mu.Unlock()
	log.Printf("terminal opened for viewer %d (%s)", vid, strings.Join(cmd.Args, " "))

	go func() {
		defer close(s.done)
		buf := make([]byte, 16*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				a.sendWait(vid, "sh-out", base64.StdEncoding.EncodeToString(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
		a.mu.Lock()
		if a.shells[vid] == s {
			delete(a.shells, vid)
		}
		a.mu.Unlock()
		a.reply(vid, "sh-exit", nil)
		log.Printf("terminal closed for viewer %d", vid)
	}()
}

// sendWait queues terminal output, waiting (bounded) instead of dropping it.
func (a *Agent) sendWait(vid int, t, d string) {
	b, _ := json.Marshal(map[string]any{"t": t, "to": vid, "d": d})
	a.mu.Lock()
	out := a.out
	a.mu.Unlock()
	if out == nil {
		return
	}
	defer func() { _ = recover() }() // the connection closed the channel meanwhile
	select {
	case out <- b:
	case <-time.After(10 * time.Second):
	}
}

func winSize(cols, rows int) *pty.Winsize {
	if cols < 10 || cols > 500 {
		cols = 80
	}
	if rows < 3 || rows > 300 {
		rows = 24
	}
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
}

func (a *Agent) shellOf(vid int) *shell {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shells[vid]
}

func (a *Agent) shellInput(vid int, d string) {
	s := a.shellOf(vid)
	if s == nil {
		return
	}
	b, err := base64.StdEncoding.DecodeString(d)
	if err == nil {
		_, _ = s.pty.Write(b)
	}
}

func (a *Agent) shellResize(vid, cols, rows int) {
	if s := a.shellOf(vid); s != nil {
		_ = pty.Setsize(s.pty, winSize(cols, rows))
	}
}

func (a *Agent) closeShell(vid int) {
	a.mu.Lock()
	s := a.shells[vid]
	delete(a.shells, vid)
	a.mu.Unlock()
	if s == nil {
		return
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.pty.Close()
}

func (a *Agent) closeShells() {
	a.mu.Lock()
	vids := make([]int, 0, len(a.shells))
	for v := range a.shells {
		vids = append(vids, v)
	}
	a.mu.Unlock()
	for _, v := range vids {
		a.closeShell(v)
	}
}
