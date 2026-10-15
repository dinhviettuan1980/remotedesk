package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// While the app is fullscreen and the page is controlling a remote machine,
// keys Windows would act on itself (Win, Alt+Tab, Alt+Esc, Ctrl+Esc, Alt+Space)
// go to the remote machine instead, like a browser's keyboard lock. The hook
// swallows them and hands them to the page as synthetic key events.
// Ctrl+Alt+Enter leaves fullscreen (Ctrl+Alt+Del and Win+L cannot be caught).

var (
	procSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procGetModuleHandle     = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

const (
	whKeyboardLL  = 13
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	llkhfInjected = 0x10
)

type kbdllhookstruct struct {
	VkCode, ScanCode, Flags, Time uint32
	ExtraInfo                     uintptr
}

// Virtual-key code → KeyboardEvent.code (US layout positions).
var vkCode = map[uint32]string{
	0x08: "Backspace", 0x09: "Tab", 0x0D: "Enter", 0x13: "Pause", 0x14: "CapsLock", 0x1B: "Escape", 0x20: "Space",
	0x21: "PageUp", 0x22: "PageDown", 0x23: "End", 0x24: "Home",
	0x25: "ArrowLeft", 0x26: "ArrowUp", 0x27: "ArrowRight", 0x28: "ArrowDown",
	0x2C: "PrintScreen", 0x2D: "Insert", 0x2E: "Delete",
	0x5B: "MetaLeft", 0x5C: "MetaRight", 0x5D: "ContextMenu",
	0xA0: "ShiftLeft", 0xA1: "ShiftRight", 0xA2: "ControlLeft", 0xA3: "ControlRight", 0xA4: "AltLeft", 0xA5: "AltRight",
	0x6A: "NumpadMultiply", 0x6B: "NumpadAdd", 0x6D: "NumpadSubtract", 0x6E: "NumpadDecimal", 0x6F: "NumpadDivide",
	0xBA: "Semicolon", 0xBB: "Equal", 0xBC: "Comma", 0xBD: "Minus", 0xBE: "Period", 0xBF: "Slash", 0xC0: "Backquote",
	0xDB: "BracketLeft", 0xDC: "Backslash", 0xDD: "BracketRight", 0xDE: "Quote",
}

func init() {
	for c := uint32('A'); c <= 'Z'; c++ {
		vkCode[c] = "Key" + string(rune(c))
	}
	for c := uint32('0'); c <= '9'; c++ {
		vkCode[c] = "Digit" + string(rune(c))
		vkCode[0x60+c-'0'] = "Numpad" + string(rune(c))
	}
	for i := uint32(1); i <= 12; i++ {
		vkCode[0x6F+i] = fmt.Sprintf("F%d", i)
	}
}

type keyHook struct {
	hwnd    uintptr
	active  func() bool // fullscreen and the page wants keys
	forward func(code string, down, ctrl, alt, shift, meta bool)
	exit    func()          // Ctrl+Alt+Enter
	held    map[uint32]bool // swallowed on the way down: the keyup is swallowed too
	drop    map[uint32]bool // swallowed and not forwarded (the exit shortcut)
}

func asyncDown(vk uintptr) bool {
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

// A modifier is down if Windows saw it go down, or we swallowed it on its way down.
func (k *keyHook) mod(generic uintptr, left, right uint32) bool {
	return asyncDown(generic) || k.held[left] || k.held[right]
}

func (k *keyHook) win() bool { return k.held[0x5B] || k.held[0x5C] }

func (k *keyHook) install() {
	k.held, k.drop = map[uint32]bool{}, map[uint32]bool{}
	mod, _, _ := procGetModuleHandle.Call(0)
	procSetWindowsHookEx.Call(whKeyboardLL, syscall.NewCallback(k.proc), mod, 0)
}

func (k *keyHook) proc(nCode int, wParam, lParam uintptr) uintptr {
	if nCode == 0 && k.handle(wParam, (*kbdllhookstruct)(unsafe.Pointer(lParam))) {
		return 1
	}
	r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return r
}

// handle reports whether the key was swallowed.
func (k *keyHook) handle(msg uintptr, h *kbdllhookstruct) bool {
	down := msg == wmKeyDown || msg == wmSysKeyDown
	vk := h.VkCode
	if !down { // finish what we started, even if fullscreen ended meanwhile
		if k.drop[vk] {
			delete(k.drop, vk)
			return true
		}
		if k.held[vk] {
			delete(k.held, vk)
			k.send(vk, false)
			return true
		}
		return false
	}
	if k.held[vk] { // auto-repeat
		k.send(vk, true)
		return true
	}
	if h.Flags&llkhfInjected != 0 {
		return false
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg != k.hwnd || !k.active() {
		return false
	}
	ctrl, alt := k.mod(0x11, 0xA2, 0xA3), k.mod(0x12, 0xA4, 0xA5)
	switch {
	case vk == 0x0D && ctrl && alt:
		k.drop[vk] = true
		k.exit()
		return true
	case vk == 0x5B || vk == 0x5C,
		k.win(), // Win+anything: Windows never saw the Win key, so neither would the page
		alt && (vk == 0x09 || vk == 0x1B || vk == 0x20),
		ctrl && vk == 0x1B:
	default:
		return false
	}
	k.held[vk] = true
	k.send(vk, true)
	return true
}

func (k *keyHook) send(vk uint32, down bool) {
	code, ok := vkCode[vk]
	if !ok {
		return
	}
	k.forward(code, down, k.mod(0x11, 0xA2, 0xA3), k.mod(0x12, 0xA4, 0xA5), k.mod(0x10, 0xA0, 0xA1), k.win())
}
