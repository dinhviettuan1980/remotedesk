package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StreamParams struct {
	FPS  int `json:"fps"`
	MaxW int `json:"maxw"`
	Kbps int `json:"kbps"`
}

func (p StreamParams) clamp() StreamParams {
	lim := func(v, lo, hi, def int) int {
		if v == 0 {
			return def
		}
		return max(lo, min(hi, v))
	}
	return StreamParams{FPS: lim(p.FPS, 5, 60, 30), MaxW: lim(p.MaxW, 640, 3840, 1920), Kbps: lim(p.Kbps, 300, 20000, 3500)}
}

// gopSeconds: keyframe interval. Viewers joining or switching to P2P wait at
// most this long for a picture without restarting the encoder.
const gopSeconds = 3

// pipeline is one way to get H.264 out of ffmpeg: a capture input plus an encoder.
type pipeline struct {
	capture string // label, e.g. "avfoundation", "ddagrab"
	encoder string
	input   []string // ffmpeg args before the output options
	filter  string   // pre-scale filter chain (may be empty)
}

func (p pipeline) String() string { return p.capture + "+" + p.encoder }

func (p pipeline) args(sp StreamParams) []string {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-fflags", "nobuffer"}
	a = append(a, p.input...)
	// Cap the rate (avfoundation delivers 60 fps whatever -framerate says),
	// then even width ≤ maxw, height keeps aspect (and stays even).
	vf := fmt.Sprintf("fps=%d,scale='trunc(min(%d,iw)/2)*2':-2:flags=bilinear", sp.FPS, sp.MaxW)
	if p.filter != "" {
		vf = p.filter + "," + vf
	}
	pixfmt := "yuv420p"
	if p.encoder != "libx264" {
		pixfmt = "nv12"
	}
	vf += ",format=" + pixfmt
	gop := strconv.Itoa(sp.FPS * gopSeconds)
	kb := func(k int) string { return strconv.Itoa(k) + "k" }
	a = append(a, "-an", "-vf", vf, "-c:v", p.encoder)
	a = append(a, encoderArgs(p.encoder, sp)...)
	a = append(a, "-b:v", kb(sp.Kbps), "-maxrate", kb(sp.Kbps*3/2), "-bufsize", kb(sp.Kbps/2),
		"-g", gop, "-bf", "0", "-flush_packets", "1", "-f", "h264", "pipe:1")
	return a
}

func encoderArgs(enc string, sp StreamParams) []string {
	switch enc {
	case "libx264":
		return []string{"-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "high"}
	case "h264_videotoolbox":
		return []string{"-realtime", "1", "-prio_speed", "1", "-profile:v", "high", "-allow_sw", "1"}
	case "h264_nvenc":
		return []string{"-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-rc", "cbr", "-delay", "0", "-profile:v", "high"}
	case "h264_qsv":
		return []string{"-preset", "veryfast", "-async_depth", "1", "-low_power", "0", "-profile:v", "high"}
	case "h264_amf":
		return []string{"-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr", "-profile:v", "high"}
	case "h264_mf":
		return []string{"-hw_encoding", "1", "-scenario", "display_remoting", "-rate_control", "cbr"}
	}
	return nil
}

// ---------------------------------------------------------------- encoder detection

var (
	encOnce  sync.Once
	encFound []string
)

// usableEncoders runs a tiny test encode with each candidate so we only try
// hardware encoders that actually work on this machine (e.g. nvenc needs an NVIDIA GPU).
func usableEncoders(ff string, cfg *Config) []string {
	encOnce.Do(func() {
		var cands []string
		if cfg.Encoder != "" && cfg.Encoder != "auto" {
			cands = []string{cfg.Encoder}
		} else {
			cands = platformEncoders()
		}
		for _, enc := range cands {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=30",
				"-frames:v", "10", "-vf", "format=" + map[bool]string{true: "yuv420p", false: "nv12"}[enc == "libx264"],
				"-c:v", enc}
			args = append(args, encoderArgs(enc, StreamParams{30, 1280, 2000})...)
			args = append(args, "-b:v", "2000k", "-f", "null", "-")
			cmd := exec.CommandContext(ctx, ff, args...)
			hideWindow(cmd)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			cancel()
			if err == nil {
				encFound = append(encFound, enc)
				log.Printf("encoder %s: ok", enc)
			} else {
				log.Printf("encoder %s: unavailable (%s)", enc, firstLine(stderr.String()))
			}
		}
		if len(encFound) == 0 {
			encFound = []string{"libx264"}
		}
	})
	return encFound
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// ---------------------------------------------------------------- session

// Session owns one running ffmpeg and turns its output into frames.
type Session struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// lastGood remembers which pipeline worked so the next session starts with it.
var (
	lastGoodMu sync.Mutex
	lastGood   string
)

type frameSink func(au []byte, key bool)

// startSession tries each pipeline until one produces a frame. onReady is
// called with the pipeline that works, onFail if none does.
func startSession(ff string, cfg *Config, sp StreamParams, sink frameSink, onReady func(pipeline), onFail func(error)) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		if err := capturePreflight(); err != nil {
			onFail(err)
			return
		}
		pipes := platformPipelines(ff, cfg, usableEncoders(ff, cfg), sp)
		lastGoodMu.Lock()
		lg := lastGood
		lastGoodMu.Unlock()
		for i, p := range pipes {
			if p.String() == lg && i > 0 {
				pipes[0], pipes[i] = pipes[i], pipes[0]
			}
		}
		var errs []string
		for _, p := range pipes {
			if ctx.Err() != nil {
				return
			}
			started, err := runPipeline(ctx, ff, p, sp, sink, func() {
				lastGoodMu.Lock()
				lastGood = p.String()
				lastGoodMu.Unlock()
				onReady(p)
			})
			if started || ctx.Err() != nil {
				if err != nil && ctx.Err() == nil {
					onFail(fmt.Errorf("%s stopped: %v", p, err))
				}
				return
			}
			log.Printf("pipeline %s failed: %v", p, err)
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
		onFail(errors.New(strings.Join(errs, "\n")))
	}()
	return s
}

func (s *Session) Stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

// runPipeline runs ffmpeg until ctx is cancelled or it exits. started reports
// whether at least one frame came out (i.e. this pipeline works).
func runPipeline(ctx context.Context, ff string, p pipeline, sp StreamParams, sink frameSink, onFirst func()) (started bool, err error) {
	args := p.args(sp)
	log.Printf("starting %s: ffmpeg %s", p, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, ff, args...)
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return false, err
	}

	chunks := make(chan []byte, 64)
	go func() {
		defer close(chunks)
		for {
			b := make([]byte, 256*1024)
			n, err := stdout.Read(b)
			if n > 0 {
				chunks <- b[:n]
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("ffmpeg read: %v", err)
				}
				return
			}
		}
	}()

	firstFrame := false
	split := &auSplitter{emit: func(au []byte, key bool) {
		if !firstFrame {
			if !key {
				return // never hand the viewer a stream that starts mid-GOP
			}
			firstFrame = true
			onFirst()
		}
		sink(au, key)
	}}

	// Generous first-frame timeout: macOS can take a while to open capture.
	firstDeadline := time.NewTimer(12 * time.Second)
	defer firstDeadline.Stop()
	idle := time.NewTimer(time.Hour)
	defer idle.Stop()
	for {
		select {
		case b, ok := <-chunks:
			if !ok {
				split.idle()
				werr := cmd.Wait()
				if ctx.Err() != nil {
					return firstFrame, nil
				}
				msg := firstLine(stderr.String())
				if msg == "" && werr != nil {
					msg = werr.Error()
				}
				return firstFrame, fmt.Errorf("ffmpeg exited: %s", msg)
			}
			split.feed(b)
			idle.Reset(4 * time.Millisecond)
		case <-idle.C:
			split.idle()
		case <-firstDeadline.C:
			if !firstFrame {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				msg := firstLine(stderr.String())
				if msg == "" {
					msg = "no video within 12s"
				}
				return false, errors.New(msg)
			}
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return firstFrame, nil
		}
	}
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// ---------------------------------------------------------------- self test

func selfTest(cfg *Config, ff string) int {
	encs := usableEncoders(ff, cfg)
	log.Printf("usable encoders: %v", encs)
	sp := StreamParams{FPS: 30, MaxW: 1920, Kbps: 3500}
	var mu sync.Mutex
	frames, keys, bytesOut := 0, 0, 0
	done := make(chan error, 1)
	var chosen string
	s := startSession(ff, cfg, sp, func(au []byte, key bool) {
		mu.Lock()
		frames++
		bytesOut += len(au)
		if key {
			keys++
		}
		mu.Unlock()
	}, func(p pipeline) {
		chosen = p.String()
		log.Printf("capture running with %s", chosen)
	}, func(err error) { done <- err })
	select {
	case err := <-done:
		log.Printf("FAILED: %v", err)
		return 1
	case <-time.After(18 * time.Second):
	}
	s.Stop()
	mu.Lock()
	defer mu.Unlock()
	if frames == 0 {
		log.Printf("FAILED: no frames captured")
		return 1
	}
	log.Printf("OK: %s produced %d frames (%d keyframes, %d KB) — input %s", chosen, frames, keys, bytesOut/1024, describeInput())
	return 0
}
