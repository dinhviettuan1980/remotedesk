package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procAttachConsole           = kernel32.NewProc("AttachConsole")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	procGlobalAlloc             = kernel32.NewProc("GlobalAlloc")
	procGlobalFree              = kernel32.NewProc("GlobalFree")
	procGlobalLock              = kernel32.NewProc("GlobalLock")
	procGlobalUnlock            = kernel32.NewProc("GlobalUnlock")

	procSetDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
)

const (
	createNoWindow   = 0x08000000
	detachedProcess  = 0x00000008
	cfUnicodeText    = 13
	gmemMoveable     = 0x0002
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
	esDisplayReq     = 0x00000002
)

func platformInit() {
	// Per-monitor DPI awareness so screen metrics are in physical pixels.
	if procSetDpiAwarenessContext.Find() == nil {
		procSetDpiAwarenessContext.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	} else {
		procSetProcessDPIAware.Call()
	}
}

// attachConsole lets the GUI-subsystem binary print when run from a terminal (-check, -version).
func attachConsole() {
	r, _, _ := procAttachConsole.Call(^uintptr(0)) // ATTACH_PARENT_PROCESS
	if r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
		log.SetOutput(f)
	}
}

func capturePreflight() error { return nil }

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func restartSelf() {
	exe, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNoWindow}
		if err := cmd.Start(); err != nil {
			log.Printf("restart: %v", err)
		}
	}
	os.Exit(0)
}

func platformEncoders() []string {
	return []string{"h264_nvenc", "h264_qsv", "h264_amf", "libx264"}
}

func platformPipelines(ff string, cfg *Config, encoders []string, sp StreamParams) []pipeline {
	fps := strconv.Itoa(sp.FPS)
	d := sp.disp
	dda := func(enc string) pipeline {
		return pipeline{
			capture: "ddagrab", encoder: enc,
			input:  []string{"-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%s:draw_mouse=1", d.capIdx, fps)},
			filter: "hwdownload,format=bgra",
		}
	}
	gdi := func(enc string) pipeline {
		x, y, w, h := int(d.X), int(d.Y), d.PxW, d.PxH
		if w <= 0 || h <= 0 { // no display list: the primary monitor
			sw, _, _ := procGetSystemMetrics.Call(0)
			sh, _, _ := procGetSystemMetrics.Call(1)
			x, y, w, h = 0, 0, int(sw), int(sh)
		}
		return pipeline{
			capture: "gdigrab", encoder: enc,
			input: []string{"-f", "gdigrab", "-framerate", fps, "-draw_mouse", "1",
				"-offset_x", strconv.Itoa(x), "-offset_y", strconv.Itoa(y), "-video_size", fmt.Sprintf("%dx%d", w, h), "-i", "desktop"},
		}
	}
	var out []pipeline
	captures := []string{"ddagrab", "gdigrab"}
	if cfg.Capture == "ddagrab" || cfg.Capture == "gdigrab" {
		captures = []string{cfg.Capture}
	}
	for _, c := range captures {
		if c == "ddagrab" && d.capIdx < 0 {
			continue // a monitor on another GPU: ddagrab only sees the default adapter
		}
		for _, enc := range encoders {
			if c == "ddagrab" {
				out = append(out, dda(enc))
			} else {
				out = append(out, gdi(enc))
			}
		}
	}
	return out
}

func captureHint() string {
	return "Windows: màn hình khoá / UAC không chụp được — hãy mở khoá máy. Kiểm tra agent.log trong C:\\ProgramData\\RemoteDesk."
}

// keepAwake holds ES_DISPLAY_REQUIRED on a dedicated OS thread (the state is per-thread).
func keepAwake() func() {
	stop := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		procSetThreadExecutionState.Call(esContinuous | esSystemRequired | esDisplayReq)
		<-stop
		procSetThreadExecutionState.Call(esContinuous)
	}()
	return func() { close(stop) }
}

// ---------------------------------------------------------------- clipboard

func openClipboard() error {
	for i := 0; i < 10; i++ { // another app may hold it briefly
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("OpenClipboard failed")
}

func clipboardRead() (string, error) {
	if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)
	var u []uint16
	for ptr := p; ; ptr += 2 {
		c := *(*uint16)(unsafe.Pointer(ptr))
		if c == 0 || len(u) > 1<<20 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), nil
}

func clipboardWrite(text string) error {
	u := utf16.Encode([]rune(text))
	u = append(u, 0)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("SetClipboardData failed")
	}
	return nil // the system owns h now
}
