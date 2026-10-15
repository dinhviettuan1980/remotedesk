package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework CoreMedia -framework ScreenCaptureKit
#include <stdlib.h>

int rd_audio_available(void);
char *rd_audio_start(void);
void rd_audio_stop(void);
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

var (
	macAudioMu   sync.Mutex // one capture at a time
	macAudioCbMu sync.Mutex
	macAudioPCM  func([]byte)
	macAudioErr  func(error)
)

func audioSupported() bool { return C.rd_audio_available() == 1 }

// startAudioCapture: ScreenCaptureKit system audio, 48 kHz stereo float32.
func startAudioCapture(pcm func([]byte), failed func(error)) (pcmFormat, func(), error) {
	macAudioMu.Lock()
	macAudioCbMu.Lock()
	macAudioPCM, macAudioErr = pcm, failed
	macAudioCbMu.Unlock()
	if msg := C.rd_audio_start(); msg != nil {
		err := errors.New(C.GoString(msg))
		C.free(unsafe.Pointer(msg))
		clearMacAudio()
		macAudioMu.Unlock()
		return pcmFormat{}, nil, err
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			C.rd_audio_stop()
			clearMacAudio()
			macAudioMu.Unlock()
		})
	}
	return pcmFormat{Rate: 48000, Channels: 2, Bits: 32, Float: true}, stop, nil
}

func clearMacAudio() {
	macAudioCbMu.Lock()
	macAudioPCM, macAudioErr = nil, nil
	macAudioCbMu.Unlock()
}

//export rdAudioPCM
func rdAudioPCM(p *C.float, frames C.int) {
	b := C.GoBytes(unsafe.Pointer(p), frames*2*4)
	macAudioCbMu.Lock()
	f := macAudioPCM
	macAudioCbMu.Unlock()
	if f != nil {
		f(b)
	}
}

//export rdAudioStopped
func rdAudioStopped(msg *C.char) {
	err := errors.New("ScreenCaptureKit: " + C.GoString(msg))
	macAudioCbMu.Lock()
	f := macAudioErr
	macAudioCbMu.Unlock()
	if f != nil {
		f(err)
	}
}
