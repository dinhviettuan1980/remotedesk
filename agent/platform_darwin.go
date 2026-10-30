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

// avfoundationScreens maps CGGetActiveDisplayList order ("Capture screen N")
// to ffmpeg's avfoundation device indices. avfoundation indices shift when
// cameras or virtual displays come and go, so it is re-queried on every
// (re)start instead of cached: a stale index fails with "Invalid device index".
func avfoundationScreens(ff string) []string {
	cmd := exec.Command(ff, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	var out bytes.Buffer
	cmd.Stderr = &out
	_ = cmd.Run()
	re := regexp.MustCompile(`\[(\d+)\] Capture screen (\d+)`)
	byScreen := map[int]string{}
	maxN := -1
	for _, m := range re.FindAllStringSubmatch(out.String(), -1) {
		n, _ := strconv.Atoi(m[2])
		byScreen[n] = m[1]
		if n > maxN {
			maxN = n
		}
	}
	screens := make([]string, maxN+1)
	for n, idx := range byScreen {
		screens[n] = idx
	}
	log.Printf("avfoundation screens: %v", screens)
	return screens
}

func platformPipelines(ff string, cfg *Config, encoders []string, sp StreamParams) []pipeline {
	screens := avfoundationScreens(ff)
	dev := ""
	if i := sp.disp.capIdx; i >= 0 && i < len(screens) && screens[i] != "" {
		dev = screens[i]
	} else {
		for _, d := range screens {
			if d != "" {
				dev = d
				break
			}
		}
	}
	if dev == "" {
		return nil // no "Capture screen" device: surface a clear error instead of guessing an index
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

// notifyUser shows a macOS notification (attributed to Script Editor).
func notifyUser(msg string) {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(msg)
	_ = exec.Command("osascript", "-e", `display notification "`+q+`" with title "RemoteDesk"`).Run()
}

// rebootMachine asks macOS to restart like the Apple menu does (apps may ask
// to save; the first time macOS asks to allow controlling System Events).
func rebootMachine() error {
	out, err := exec.Command("osascript", "-e", `tell application "System Events" to restart`).CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out)))
	}
	return nil
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
