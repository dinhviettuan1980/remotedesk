package main

// System audio on Windows: WASAPI loopback on the default output device, in
// its mix format (usually 48 kHz float32 stereo; ffmpeg resamples/downmixes).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = guid{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	clsctxAll              = 0x17
	audclntStreamLoopback  = 0x00020000
	audclntBufferSilent    = 0x2
	waveFormatPCM          = 1
	waveFormatFloat        = 3
	waveFormatExtensible   = 0xFFFE
	eRender, eConsole      = 0, 0
	loopbackBuffer100ns    = 2_000_000 // 200 ms shared buffer
	defaultDeviceCheckTime = 2 * time.Second
)

func audioSupported() bool { return ole32.Load() == nil }

func hrErr(what string, hr uintptr) error {
	return fmt.Errorf("%s: HRESULT 0x%08X", what, uint32(hr))
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// startAudioCapture runs the capture on its own COM thread until stop().
func startAudioCapture(pcm func([]byte), onErr func(error)) (pcmFormat, func(), error) {
	type result struct {
		f   pcmFormat
		err error
	}
	ready := make(chan result, 1)
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if hr, _, _ := procCoInitializeEx.Call(0, 0); !failed(hr) { // COINIT_MULTITHREADED
			defer procCoUninitialize.Call()
		}
		lb, f, err := openLoopback()
		ready <- result{f, err}
		if err != nil {
			return
		}
		defer lb.close()
		if err := lb.run(quit, f, pcm); err != nil {
			onErr(err)
		}
	}()
	r := <-ready
	if r.err != nil {
		<-done
		return pcmFormat{}, nil, r.err
	}
	var once sync.Once
	return r.f, func() { once.Do(func() { close(quit); <-done }) }, nil
}

type loopback struct {
	enum, dev, client, capture uintptr
	devID                      string
}

func openLoopback() (lb *loopback, f pcmFormat, err error) {
	lb = &loopback{}
	defer func() {
		if err != nil {
			lb.close()
		}
	}()
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&lb.enum))); failed(hr) {
		return lb, f, hrErr("MMDeviceEnumerator", hr)
	}
	if hr := comCall(lb.enum, 4, eRender, eConsole, uintptr(unsafe.Pointer(&lb.dev))); failed(hr) { // GetDefaultAudioEndpoint
		return lb, f, errors.New("không có thiết bị phát âm thanh")
	}
	lb.devID = deviceID(lb.dev)
	if hr := comCall(lb.dev, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&lb.client))); failed(hr) { // Activate
		return lb, f, hrErr("IMMDevice::Activate", hr)
	}
	var wf uintptr
	if hr := comCall(lb.client, 8, uintptr(unsafe.Pointer(&wf))); failed(hr) || wf == 0 { // GetMixFormat
		return lb, f, hrErr("GetMixFormat", hr)
	}
	defer procCoTaskMemFree.Call(wf)
	if f, err = parseWaveFormat(wf); err != nil {
		return lb, f, err
	}
	if hr := comCall(lb.client, 3, 0, audclntStreamLoopback, loopbackBuffer100ns, 0, wf, 0); failed(hr) { // Initialize (shared)
		return lb, f, hrErr("IAudioClient::Initialize", hr)
	}
	if hr := comCall(lb.client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&lb.capture))); failed(hr) { // GetService
		return lb, f, hrErr("GetService(IAudioCaptureClient)", hr)
	}
	if hr := comCall(lb.client, 10); failed(hr) { // Start
		return lb, f, hrErr("IAudioClient::Start", hr)
	}
	return lb, f, nil
}

// parseWaveFormat reads a WAVEFORMATEX(TENSIBLE) (packed: read by offset).
func parseWaveFormat(p uintptr) (pcmFormat, error) {
	b := unsafe.Slice((*byte)(unsafe.Pointer(p)), 18)
	le := binary.LittleEndian
	tag := le.Uint16(b[0:])
	f := pcmFormat{Channels: int(le.Uint16(b[2:])), Rate: int(le.Uint32(b[4:])), Bits: int(le.Uint16(b[14:]))}
	if tag == waveFormatExtensible && le.Uint16(b[16:]) >= 22 {
		ext := unsafe.Slice((*byte)(unsafe.Pointer(p)), 40)
		tag = uint16(le.Uint32(ext[24:])) // SubFormat.Data1: 1 = PCM, 3 = IEEE float
	}
	switch {
	case tag == waveFormatFloat && f.Bits == 32:
		f.Float = true
	case tag == waveFormatPCM && (f.Bits == 16 || f.Bits == 24 || f.Bits == 32):
	default:
		return f, fmt.Errorf("định dạng âm thanh không hỗ trợ (tag %d, %d bit)", tag, f.Bits)
	}
	if f.Channels <= 0 || f.Rate <= 0 {
		return f, errors.New("định dạng âm thanh không hợp lệ")
	}
	return f, nil
}

// run polls the capture buffer every 10 ms until quit. It returns
// errAudioDeviceChanged when the default output device changed (headphones
// plugged in…): the loopback would keep listening to the old one.
func (lb *loopback) run(quit chan struct{}, f pcmFormat, pcm func([]byte)) error {
	fb := f.frameBytes()
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	lastCheck := time.Now()
	for {
		select {
		case <-quit:
			return nil
		case <-t.C:
		}
		for {
			var n uint32
			if hr := comCall(lb.capture, 5, uintptr(unsafe.Pointer(&n))); failed(hr) { // GetNextPacketSize
				return hrErr("GetNextPacketSize", hr)
			}
			if n == 0 {
				break
			}
			var data uintptr
			var frames, flags uint32
			if hr := comCall(lb.capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0); failed(hr) { // GetBuffer
				return hrErr("GetBuffer", hr)
			}
			b := make([]byte, int(frames)*fb)
			if flags&audclntBufferSilent == 0 && data != 0 && len(b) > 0 {
				copy(b, unsafe.Slice((*byte)(unsafe.Pointer(data)), len(b)))
			}
			comCall(lb.capture, 4, uintptr(frames)) // ReleaseBuffer
			if len(b) > 0 {
				pcm(b)
			}
		}
		if time.Since(lastCheck) > defaultDeviceCheckTime {
			lastCheck = time.Now()
			if id := defaultRenderID(lb.enum); id != "" && id != lb.devID {
				return errAudioDeviceChanged
			}
		}
	}
}

func (lb *loopback) close() {
	if lb.client != 0 {
		comCall(lb.client, 11) // Stop
	}
	comRelease(lb.capture)
	comRelease(lb.client)
	comRelease(lb.dev)
	comRelease(lb.enum)
	*lb = loopback{}
}

func defaultRenderID(enum uintptr) string {
	var dev uintptr
	if hr := comCall(enum, 4, eRender, eConsole, uintptr(unsafe.Pointer(&dev))); failed(hr) || dev == 0 {
		return ""
	}
	defer comRelease(dev)
	return deviceID(dev)
}

// deviceID: IMMDevice::GetId.
func deviceID(dev uintptr) string {
	var p uintptr
	if hr := comCall(dev, 5, uintptr(unsafe.Pointer(&p))); failed(hr) || p == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(p)
	var u []uint16
	for ptr := p; len(u) < 1024; ptr += 2 {
		c := *(*uint16)(unsafe.Pointer(ptr))
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return syscall.UTF16ToString(u)
}
