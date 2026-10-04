package main

import (
	"os/exec"
	"testing"
)

// Encodes 60 test frames with each encoder, feeds the byte stream in odd-sized
// chunks, and checks we get back exactly 60 access units with the IDRs flagged.
func TestSplitterRealEncoders(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, enc := range []string{"libx264", "h264_videotoolbox"} {
		p := pipeline{encoder: enc, input: []string{"-f", "lavfi", "-i", "testsrc2=s=1920x1080:r=30", "-frames:v", "60"}}
		args := p.args(StreamParams{FPS: 30, MaxW: 1920, Kbps: 4000})
		out, err := exec.Command(ff, args...).Output()
		if err != nil {
			t.Logf("%s unavailable: %v", enc, err)
			continue
		}
		var frames, keys int
		s := &auSplitter{emit: func(au []byte, key bool) {
			frames++
			if key {
				keys++
			}
		}}
		for i := 0; i < len(out); i += 7777 {
			s.feed(out[i:min(len(out), i+7777)])
		}
		s.idle()
		if frames != 60 || keys != 1 {
			t.Errorf("%s: got %d frames, %d keyframes; want 60, 1", enc, frames, keys)
		}
	}
}
