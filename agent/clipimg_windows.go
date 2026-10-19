package main

// Clipboard images on Windows: the registered "PNG" format when an app put
// one there (browsers, Snipping Tool…), else CF_DIB converted to PNG. Writing
// sets both, so every app finds something it can paste.

import (
	"bytes"
	"errors"
	"image/png"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	procGetClipboardSeq    = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipFormat = user32.NewProc("RegisterClipboardFormatW")
	procGlobalSize         = kernel32.NewProc("GlobalSize")
	cfPNG                  = registerClipFormat("PNG")
)

const (
	cfDIB   = 8
	cfHDROP = 15
)

func registerClipFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegisterClipFormat.Call(uintptr(unsafe.Pointer(p)))
	return r
}

func clipboardSeq() uint64 {
	r, _, _ := procGetClipboardSeq.Call()
	return uint64(r)
}

func clipAvailable(f uintptr) bool {
	r, _, _ := procIsClipboardFormatAvail.Call(f)
	return r != 0
}

// clipboardImage returns the clipboard image as PNG when it holds an image and
// no text or files (Office adds a picture of copied text and cells).
func clipboardImage() ([]byte, error) {
	if clipAvailable(cfUnicodeText) || clipAvailable(cfHDROP) {
		return nil, nil
	}
	hasPNG := cfPNG != 0 && clipAvailable(cfPNG)
	if !hasPNG && !clipAvailable(cfDIB) {
		return nil, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return nil, err
	}
	defer procCloseClipboard.Call()
	if hasPNG {
		if b := globalBytes(cfPNG); len(b) > 0 {
			return b, nil
		}
	}
	dib := globalBytes(cfDIB)
	if dib == nil {
		return nil, errors.New("could not read the clipboard image")
	}
	return dibToPNG(dib)
}

// globalBytes copies one clipboard format (clipboard must be open).
func globalBytes(format uintptr) []byte {
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil
	}
	n, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 || n == 0 || n > 64<<20 {
		if p != 0 {
			procGlobalUnlock.Call(h)
		}
		return nil
	}
	defer procGlobalUnlock.Call(h)
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), n)...)
}

func clipboardWriteImage(data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return errClipboardNotAnImage
	}
	dib := imageToDIB(img)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if err := setClipBytes(cfDIB, dib); err != nil {
		return err
	}
	if cfPNG != 0 {
		_ = setClipBytes(cfPNG, data)
	}
	return nil
}

// setClipBytes hands a copy of b to the clipboard (clipboard must be open).
func setClipBytes(format uintptr, b []byte) error {
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)), b)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(format, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("SetClipboardData failed")
	}
	return nil
}
