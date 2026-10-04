package main

import (
	"errors"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const cfHDrop = 15

var procDragQueryFileW = syscall.NewLazyDLL("shell32.dll").NewProc("DragQueryFileW")

// clipboardReadFiles returns the files copied in Explorer (CF_HDROP), if any.
func clipboardReadFiles() ([]string, error) {
	if r, _, _ := procIsClipboardFormatAvail.Call(cfHDrop); r == 0 {
		return nil, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return nil, err
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return nil, nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var out []string
	for i := uintptr(0); i < n && i < 1000; i++ {
		l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		out = append(out, syscall.UTF16ToString(buf))
	}
	return out, nil
}

// clipboardWriteFiles puts files on the clipboard as Explorer does, so
// Ctrl+V in a folder copies them there.
func clipboardWriteFiles(paths []string) error {
	if len(paths) == 0 {
		return errors.New("no files")
	}
	// DROPFILES {pFiles DWORD; pt POINT; fNC BOOL; fWide BOOL} then a
	// double-NUL-terminated UTF-16 list.
	const hdr = 20
	var list []uint16
	for _, p := range paths {
		list = append(list, utf16.Encode([]rune(p))...)
		list = append(list, 0)
	}
	list = append(list, 0)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(hdr+len(list)*2))
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(p)), hdr)
	clear(b)
	*(*uint32)(unsafe.Pointer(p)) = hdr    // pFiles
	*(*uint32)(unsafe.Pointer(p + 16)) = 1 // fWide
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p+hdr)), len(list)), list)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(cfHDrop, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("SetClipboardData failed")
	}
	return nil
}
