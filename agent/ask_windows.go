package main

import (
	"log"
	"syscall"
	"time"
	"unsafe"
)

var procMessageBoxTimeoutW = user32.NewProc("MessageBoxTimeoutW")

const (
	mbYesNo        = 0x00000004
	mbIconQuestion = 0x00000020
	mbDefButton2   = 0x00000100
	mbSystemModal  = 0x00001000
	mbSetForegrnd  = 0x00010000
	mbTopmost      = 0x00040000
	idYes          = 6
)

// askUser: Yes/No box on this machine's desktop, "No" when it times out or
// cannot be shown (the agent runs in the signed-in user's session).
func askUser(title, msg string, timeout time.Duration) bool {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	r, _, err := procMessageBoxTimeoutW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)),
		mbYesNo|mbIconQuestion|mbDefButton2|mbSystemModal|mbSetForegrnd|mbTopmost, 0, uintptr(timeout.Milliseconds()))
	if r == 0 {
		log.Printf("consent dialog: %v", err)
	}
	return r == idYes
}
