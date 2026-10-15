package main

// Remote audio: what this machine plays, compressed to Opus, for viewers that
// turned 🔊 on. Off by default; the agent only captures while some viewer wants it.
//
//   relay → agent  {"t":"audio","vids":[…],"ws":bool}
//                  vids: viewers that want audio (their P2P peers get an Opus
//                  track); ws: some of them watch over the relay, so send
//                  binary type 3 there too.
//   agent → relay  [3][0][ts u32 BE][Opus packet, 20 ms]
//
// Capture is native (audio_darwin.m: ScreenCaptureKit; audio_windows.go: WASAPI
// loopback) and hands PCM to ffmpeg as a WAV stream on stdin. ffmpeg encodes
// with libopus and writes Ogg, which readOggPackets splits back into packets.
// WAV with a small -max_size keeps ffmpeg's input side at ~5 ms; raw PCM input
// is read in ~85 ms blocks.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// errAudioDeviceChanged: the capture follows the new default output device.
var errAudioDeviceChanged = errors.New("default output device changed")

// pcmFormat describes what the platform capture delivers (interleaved).
type pcmFormat struct {
	Rate     int
	Channels int
	Bits     int
	Float    bool
}

func (f pcmFormat) frameBytes() int { return f.Channels * f.Bits / 8 }

// wavHeader starts an endless WAV stream (sizes unknown: 0xFFFFFFFF).
func (f pcmFormat) wavHeader() []byte {
	h := make([]byte, 44)
	le := binary.LittleEndian
	copy(h, "RIFF")
	le.PutUint32(h[4:], 0xFFFFFFFF)
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	tag := uint16(1) // PCM
	if f.Float {
		tag = 3 // IEEE float
	}
	le.PutUint16(h[20:], tag)
	le.PutUint16(h[22:], uint16(f.Channels))
	le.PutUint32(h[24:], uint32(f.Rate))
	le.PutUint32(h[28:], uint32(f.Rate*f.frameBytes()))
	le.PutUint16(h[32:], uint16(f.frameBytes()))
	le.PutUint16(h[34:], uint16(f.Bits))
	copy(h[36:], "data")
	le.PutUint32(h[40:], 0xFFFFFFFF)
	return h
}

var (
	opusOnce sync.Once
	opusEnc  string
)

// opusEncoder finds an Opus encoder in this ffmpeg: libopus (Homebrew and the
// gyan.dev builds have it), else ffmpeg's own experimental "opus". "" = none.
func opusEncoder(ff string) string {
	opusOnce.Do(func() {
		for _, enc := range []string{"libopus", "opus"} {
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2:sample_rate=48000",
				"-ac", "2", "-c:a", enc, "-strict", "-2", "-b:a", "96k", "-f", "null", "-"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmd := exec.CommandContext(ctx, ff, args...)
			hideWindow(cmd)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			cancel()
			if err == nil {
				opusEnc = enc
				log.Printf("audio encoder: %s", enc)
				return
			}
			log.Printf("audio encoder %s: unavailable (%s)", enc, firstLine(stderr.String()))
		}
	})
	return opusEnc
}

// audioAvailable: this machine can send its sound (OS support + an Opus encoder).
func audioAvailable(ff string) bool {
	return audioSupported() && opusEncoder(ff) != ""
}

func opusArgs(enc string, f pcmFormat) []string {
	// 240 frames per demuxed packet (5 ms at 48 kHz).
	maxSize := strconv.Itoa(240 * f.frameBytes())
	a := []string{"-hide_banner", "-loglevel", "error", "-fflags", "nobuffer", "-probesize", "32", "-analyzeduration", "0",
		"-f", "wav", "-max_size", maxSize, "-i", "pipe:0",
		"-ac", "2", "-ar", "48000", "-c:a", enc, "-b:a", "96k"}
	if enc == "libopus" {
		a = append(a, "-application", "lowdelay", "-frame_duration", "20")
	} else {
		a = append(a, "-strict", "-2")
	}
	return append(a, "-f", "ogg", "-page_duration", "20000", "-flush_packets", "1", "pipe:1")
}

// ---------------------------------------------------------------- session

type audioSession struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// startAudio captures and encodes until stopped, restarting after failures
// (the default output device changed, ffmpeg died…). onFail fires once per outage.
func startAudio(ff string, sink func(pkt []byte), onFail func(error)) *audioSession {
	ctx, cancel := context.WithCancel(context.Background())
	s := &audioSession{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		wait := time.Second
		failed := false
		for {
			start := time.Now()
			err := runAudio(ctx, ff, sink)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errAudioDeviceChanged) {
				log.Printf("audio: %v, restarting", err)
				continue
			}
			if time.Since(start) > 10*time.Second {
				wait, failed = time.Second, false
			}
			log.Printf("audio: %v (retry in %s)", err, wait)
			if !failed {
				onFail(err)
				failed = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > 30*time.Second {
				wait = 30 * time.Second
			}
		}
	}()
	return s
}

func (s *audioSession) Stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

func runAudio(parent context.Context, ff string, sink func([]byte)) error {
	enc := opusEncoder(ff)
	if enc == "" {
		return errors.New("ffmpeg has no Opus encoder")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	pcm := make(chan []byte, 256)
	var pacer atomic.Pointer[pcmPacer] // set once the capture told us its format
	format, stop, err := startAudioCapture(func(b []byte) {
		pacer.Load().write(b)
	}, func(err error) { cancel(err) })
	if err != nil {
		return err
	}
	defer stop()
	p := newPCMPacer(format, func(b []byte) {
		select {
		case pcm <- b:
		default: // ffmpeg is not keeping up: drop rather than add latency
		}
	})
	p.run(ctx)
	pacer.Store(p)

	args := opusArgs(enc, format)
	log.Printf("audio: %d Hz × %d ch %d-bit%s → ffmpeg %v", format.Rate, format.Channels, format.Bits, map[bool]string{true: " float"}[format.Float], args)
	cmd := exec.CommandContext(ctx, ff, args...)
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{max: 2048}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		defer stdin.Close()
		if _, err := stdin.Write(format.wavHeader()); err != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-pcm:
				if _, err := stdin.Write(b); err != nil {
					cancel(fmt.Errorf("ffmpeg stdin: %w", err))
					return
				}
			}
		}
	}()

	n := 0
	rerr := readOggPackets(stdout, func(p []byte) {
		n++
		if n <= 2 && (bytes.HasPrefix(p, []byte("OpusHead")) || bytes.HasPrefix(p, []byte("OpusTags"))) {
			return
		}
		sink(p)
	})
	cancel(nil)
	werr := cmd.Wait()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	if parent.Err() != nil {
		return nil
	}
	if msg := firstLine(stderr.String()); msg != "" {
		return errors.New("ffmpeg: " + msg)
	}
	if werr != nil {
		return werr
	}
	return rerr
}

// readOggPackets calls fn with each complete packet of an Ogg stream (fn may
// keep the slice) until the stream ends.
func readOggPackets(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	hdr := make([]byte, 27)
	var pkt []byte
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if string(hdr[:4]) != "OggS" {
			return errors.New("ogg: lost sync")
		}
		if hdr[5]&1 == 0 { // not a continued packet: drop any unfinished one
			pkt = nil
		}
		lacing := make([]byte, hdr[26])
		if _, err := io.ReadFull(br, lacing); err != nil {
			return err
		}
		size := 0
		for _, l := range lacing {
			size += int(l)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(br, body); err != nil {
			return err
		}
		off := 0
		for _, l := range lacing {
			pkt = append(pkt, body[off:off+int(l)]...)
			off += int(l)
			if l < 255 {
				fn(pkt)
				pkt = nil
			}
		}
	}
}

// ---------------------------------------------------------------- pacing

// pcmPacer passes captured PCM through and fills silence while the capture
// delivers nothing: loopback capture goes quiet when nothing plays, but the
// encoder (and the viewer's playback clock) want a steady stream.
type pcmPacer struct {
	mu     sync.Mutex
	f      pcmFormat
	out    func([]byte)
	start  time.Time
	frames int64 // frames passed on since start
}

func newPCMPacer(f pcmFormat, out func([]byte)) *pcmPacer {
	return &pcmPacer{f: f, out: out}
}

func (p *pcmPacer) due(now time.Time) int64 {
	return int64(now.Sub(p.start)) * int64(p.f.Rate) / int64(time.Second)
}

func (p *pcmPacer) write(b []byte) {
	if p == nil {
		return // capture started delivering before the pacer was set up
	}
	fb := p.f.frameBytes()
	if fb <= 0 || len(b) < fb {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.start.IsZero() {
		p.start = now
	}
	p.frames += int64(len(b) / fb)
	// The device clock runs a little fast, or a burst came in: re-anchor
	// rather than let the silence filler fall behind for good.
	if ahead := p.frames - p.due(now); ahead > int64(p.f.Rate/5) {
		p.start = now.Add(-time.Duration(p.frames) * time.Second / time.Duration(p.f.Rate))
	}
	p.out(b)
}

// run fills gaps longer than 40 ms with silence, leaving 20 ms of slack so a
// late capture buffer does not end up queued behind made-up silence.
func (p *pcmPacer) run(ctx context.Context) {
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				p.mu.Lock()
				if p.start.IsZero() {
					p.start = now
				}
				lag := p.due(now) - p.frames
				if lag > int64(p.f.Rate)*40/1000 {
					n := lag - int64(p.f.Rate)*20/1000
					p.frames += n
					p.out(make([]byte, int(n)*p.f.frameBytes()))
				}
				p.mu.Unlock()
			}
		}
	}()
}
