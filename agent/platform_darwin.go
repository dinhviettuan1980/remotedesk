package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

func platformInit() {
	// Trigger the macOS permission prompts once; they're no-ops after the first time.
	if !screenAccess(true) {
		log.Printf("Screen Recording permission missing — grant it in System Settings › Privacy & Security, then restart the agent")
	}
	if !axAccess(true) {
		log.Printf("Accessibility permission missing — grant it in System Settings › Privacy & Security (needed for mouse/keyboard)")
	}
}

func attachConsole() {}

// capturePreflight fails fast instead of letting avfoundation hang without permission.
// macOS only re-reads the grant on process start, hence the restart hint.
func capturePreflight() error {
	if !screenAccess(false) {
		return errors.New("chưa có quyền Screen Recording")
	}
	return nil
}

func hideWindow(*exec.Cmd) {}

// restartSelf exits; launchd (KeepAlive) starts a fresh process, which also
// picks up newly granted permissions.
func restartSelf() { os.Exit(0) }

func platformEncoders() []string { return []string{"h264_videotoolbox", "libx264"} }

var (
	screenIdxMu sync.Mutex
	screenIdx   []string
)

// avfoundationScreens lists ffmpeg's "Capture screen N" device indices in
// display order, asking ffmpeg again when the number of displays changed.
func avfoundationScreens(ff string, displays int) []string {
	screenIdxMu.Lock()
	defer screenIdxMu.Unlock()
	if screenIdx != nil && len(screenIdx) == displays {
		return screenIdx
	}
	cmd := exec.Command(ff, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	var out bytes.Buffer
	cmd.Stderr = &out
	_ = cmd.Run()
	re := regexp.MustCompile(`\[(\d+)\] Capture screen (\d+)`)
	screenIdx = []string{}
	for _, m := range re.FindAllStringSubmatch(out.String(), -1) {
		screenIdx = append(screenIdx, m[1])
	}
	log.Printf("avfoundation screens: %v", screenIdx)
	return screenIdx
}

func platformPipelines(ff string, cfg *Config, encoders []string, sp StreamParams) []pipeline {
	screens := avfoundationScreens(ff, len(listDisplays()))
	dev := "1"
	if i := sp.disp.capIdx; i >= 0 && i < len(screens) {
		dev = screens[i]
	} else if len(screens) > 0 {
		dev = screens[0]
	}
	var out []pipeline
	for _, pix := range []string{"nv12", ""} {
		for _, enc := range encoders {
			in := []string{"-f", "avfoundation", "-capture_cursor", "1", "-framerate", strconv.Itoa(sp.FPS)}
			if pix != "" {
				in = append(in, "-pixel_format", pix)
			}
			in = append(in, "-i", dev+":none")
			out = append(out, pipeline{capture: "avfoundation", encoder: enc, input: in})
		}
	}
	return out
}

func captureHint() string {
	return "macOS: cấp quyền Screen Recording + Accessibility cho remote-agent trong System Settings › Privacy & Security, rồi bấm “Khởi động lại agent”."
}

// keepAwake holds a caffeinate assertion (display + system) while a viewer is attached.
func keepAwake() func() {
	cmd := exec.Command("caffeinate", "-d", "-i", "-u")
	if err := cmd.Start(); err != nil {
		return func() {}
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func clipboardRead() (string, error) {
	cmd := exec.Command("pbpaste")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8") // launchd jobs have no LANG; without it non-ASCII is mangled
	out, err := cmd.Output()
	return string(out), err
}

func clipboardWrite(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8")
	return cmd.Run()
}
