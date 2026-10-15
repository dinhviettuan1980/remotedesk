package main

import (
	"bytes"
	"encoding/binary"
	"os/exec"
	"testing"
)

// oggPage builds one Ogg page holding segs (lacing values computed here).
func oggPage(continued bool, packets ...[]byte) []byte {
	var lacing, body []byte
	for i, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		// The last packet may continue on the next page: no terminating lacing value.
		if i < len(packets)-1 || n > 0 || len(p) == 0 {
			lacing = append(lacing, byte(n))
		}
		body = append(body, p...)
	}
	h := make([]byte, 27)
	copy(h, "OggS")
	if continued {
		h[5] = 1
	}
	h[26] = byte(len(lacing))
	return append(append(h, lacing...), body...)
}

func TestReadOggPackets(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 600) // spans lacing values 255, 255, 90
	split := bytes.Repeat([]byte{9}, 510)
	var stream []byte
	stream = append(stream, oggPage(false, []byte("OpusHead..."))...)
	stream = append(stream, oggPage(false, []byte("a"), big, []byte("bc"))...)
	// A packet split over two pages: exactly 2×255 bytes on the first (no
	// terminating lacing value), then the rest.
	stream = append(stream, oggPage(false, split)...)
	stream = append(stream, oggPage(true, []byte("tail"))...)

	var got [][]byte
	if err := readOggPackets(bytes.NewReader(stream), func(p []byte) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	want := [][]byte{[]byte("OpusHead..."), []byte("a"), big, []byte("bc"), append(append([]byte{}, split...), "tail"...)}
	if len(got) != len(want) {
		t.Fatalf("got %d packets, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("packet %d: got %d bytes, want %d", i, len(got[i]), len(want[i]))
		}
	}
}

func TestWavHeader(t *testing.T) {
	h := pcmFormat{Rate: 44100, Channels: 6, Bits: 32, Float: true}.wavHeader()
	le := binary.LittleEndian
	if string(h[:4]) != "RIFF" || string(h[8:16]) != "WAVEfmt " || string(h[36:40]) != "data" {
		t.Fatalf("bad header %q", h)
	}
	if le.Uint16(h[20:]) != 3 || le.Uint16(h[22:]) != 6 || le.Uint32(h[24:]) != 44100 || le.Uint16(h[32:]) != 24 || le.Uint32(h[28:]) != 44100*24 {
		t.Fatalf("bad fmt chunk % x", h[20:36])
	}
}

// The real pipeline: WAV on stdin → ffmpeg Opus/Ogg → packets (needs ffmpeg with an Opus encoder).
func TestOpusPipe(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	enc := opusEncoder(ff)
	if enc == "" {
		t.Skip("ffmpeg has no Opus encoder")
	}
	f := pcmFormat{Rate: 48000, Channels: 2, Bits: 32, Float: true}
	cmd := exec.Command(ff, opusArgs(enc, f)...)
	in := append(f.wavHeader(), make([]byte, 48000*f.frameBytes())...) // 1 s of silence
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := readOggPackets(bytes.NewReader(out), func(p []byte) { n++ }); err != nil {
		t.Fatal(err)
	}
	// OpusHead + OpusTags + ~50 packets of 20 ms.
	if n < 45 || n > 60 {
		t.Fatalf("%d packets for 1 s", n)
	}
}

func TestPickDisplay(t *testing.T) {
	list := primaryFirst([]Display{{Name: "a"}, {Name: "b", Primary: true}, {Name: "c"}})
	if list[0].Name != "b" || list[1].Name != "a" || list[2].Name != "c" {
		t.Fatalf("primaryFirst: %v", list)
	}
	if d, i := pickDisplay(list, 2); d.Name != "c" || i != 2 {
		t.Fatalf("pick 2: %v %d", d.Name, i)
	}
	if d, i := pickDisplay(list, 7); d.Name != "b" || i != 0 {
		t.Fatalf("pick out of range: %v %d", d.Name, i)
	}
	d := Display{X: -1920, Y: 0, W: 1920, H: 1080}
	if x, y, ok := d.toDisplay(-960, 540); !ok || x != 0.5 || y != 0.5 {
		t.Fatalf("toDisplay: %v %v %v", x, y, ok)
	}
	if _, _, ok := d.toDisplay(10, 10); ok {
		t.Fatal("point on another display reported as on d")
	}
}
