package main

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procSendInput        = user32.NewProc("SendInput")
	procGetGUIThreadInfo = user32.NewProc("GetGUIThreadInfo")
	procClientToScreen   = user32.NewProc("ClientToScreen")
	procGetCursorInfo    = user32.NewProc("GetCursorInfo")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseXDown      = 0x0080
	mouseXUp        = 0x0100
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	mouseAbsolute   = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyScancode = 0x0008
)

// INPUT is 40 bytes on 64-bit Windows; the union is sized by MOUSEINPUT.
type mouseInput struct {
	typ       uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keybdInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
	_     [8]byte
}

func sendMouse(mi mouseInput) {
	mi.typ = inputMouse
	procSendInput.Call(1, uintptr(unsafe.Pointer(&mi)), unsafe.Sizeof(mi))
}

func sendKey(ki keybdInput) {
	ki.typ = inputKeyboard
	procSendInput.Call(1, uintptr(unsafe.Pointer(&ki)), unsafe.Sizeof(ki))
}

type winInjector struct {
	mu      sync.Mutex
	ax, ay  int32 // last absolute position (0..65535)
	buttons map[int]bool
	keys    map[string]bool
	wheelX  float64
	wheelY  float64
}

func newInjector(display int) Injector {
	// Absolute coordinates without MOUSEEVENTF_VIRTUALDESK map onto the primary
	// monitor, which is what ddagrab output 0 / gdigrab captures.
	return &winInjector{buttons: map[int]bool{}, keys: map[string]bool{}}
}

func (w *winInjector) MouseMove(x, y float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ax, w.ay = int32(x*65535+0.5), int32(y*65535+0.5)
	sendMouse(mouseInput{dx: w.ax, dy: w.ay, flags: mouseMove | mouseAbsolute})
}

func (w *winInjector) MouseButton(b int, down bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var flags, data uint32
	switch b {
	case 0:
		flags = pick(down, mouseLeftDown, mouseLeftUp)
	case 1:
		flags = pick(down, mouseMiddleDown, mouseMiddleUp)
	case 2:
		flags = pick(down, mouseRightDown, mouseRightUp)
	case 3, 4:
		flags = pick(down, mouseXDown, mouseXUp)
		data = uint32(b - 2) // XBUTTON1 = back, XBUTTON2 = forward
	default:
		return
	}
	w.buttons[b] = down
	sendMouse(mouseInput{dx: w.ax, dy: w.ay, mouseData: data, flags: flags | mouseMove | mouseAbsolute})
}

func (w *winInjector) Wheel(dx, dy float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Browser pixels → wheel units: one 100px notch ≈ WHEEL_DELTA (120).
	w.wheelY += -dy * 1.2
	w.wheelX += dx * 1.2
	if iy := int32(w.wheelY); iy != 0 {
		w.wheelY -= float64(iy)
		sendMouse(mouseInput{mouseData: uint32(iy), flags: mouseWheel})
	}
	if ix := int32(w.wheelX); ix != 0 {
		w.wheelX -= float64(ix)
		sendMouse(mouseInput{mouseData: uint32(ix), flags: mouseHWheel})
	}
}

func (w *winInjector) Key(code string, down bool) {
	k, ok := winKeys[code]
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if down {
		w.keys[code] = true
	} else {
		delete(w.keys, code)
	}
	ki := keybdInput{}
	if k.vk != 0 {
		ki.vk = k.vk
	} else {
		ki.scan = k.scan
		ki.flags = keyScancode
		if k.ext {
			ki.flags |= keyExtended
		}
	}
	if !down {
		ki.flags |= keyUp
	}
	sendKey(ki)
}

func (w *winInjector) ReleaseAll() {
	w.mu.Lock()
	keys := make([]string, 0, len(w.keys))
	for k := range w.keys {
		keys = append(keys, k)
	}
	var buttons []int
	for b, d := range w.buttons {
		if d {
			buttons = append(buttons, b)
		}
	}
	w.mu.Unlock()
	for _, k := range keys {
		w.Key(k, false)
	}
	for _, b := range buttons {
		w.MouseButton(b, false)
	}
}

func (w *winInjector) Wake() {
	sendMouse(mouseInput{flags: mouseMove}) // zero relative move = user activity
}

type winRect struct{ left, top, right, bottom int32 }
type winPoint struct{ x, y int32 }

type guiThreadInfo struct {
	cbSize, flags                                                              uint32
	hwndActive, hwndFocus, hwndCapture, hwndMenuOwner, hwndMoveSize, hwndCaret uintptr
	rcCaret                                                                    winRect
}

type cursorInfo struct {
	cbSize, flags uint32
	hCursor       uintptr
	pt            winPoint
}

// FocusedEdit: the foreground thread's caret (classic and most native text
// fields), else an I-beam mouse pointer right after the click (browsers,
// Electron) counts as a text field under the pointer.
func (w *winInjector) FocusedEdit() (bool, float64, float64, bool) {
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN: primary monitor, what is captured
	sh, _, _ := procGetSystemMetrics.Call(1)
	norm := func(p winPoint) (float64, float64, bool) {
		if sw == 0 || sh == 0 {
			return 0, 0, false
		}
		nx, ny := float64(p.x)/float64(sw), float64(p.y)/float64(sh)
		return nx, ny, nx >= 0 && nx <= 1 && ny >= 0 && ny <= 1
	}
	gti := guiThreadInfo{}
	gti.cbSize = uint32(unsafe.Sizeof(gti))
	if r, _, _ := procGetGUIThreadInfo.Call(0, uintptr(unsafe.Pointer(&gti))); r != 0 && gti.hwndCaret != 0 {
		p := winPoint{gti.rcCaret.left, (gti.rcCaret.top + gti.rcCaret.bottom) / 2}
		procClientToScreen.Call(gti.hwndCaret, uintptr(unsafe.Pointer(&p)))
		nx, ny, ok := norm(p)
		return true, nx, ny, ok
	}
	ci := cursorInfo{}
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r != 0 {
		ibeam, _, _ := procLoadCursorW.Call(0, 32513) // IDC_IBEAM
		if ibeam != 0 && ci.hCursor == ibeam {
			nx, ny, ok := norm(ci.pt)
			return true, nx, ny, ok
		}
	}
	return false, 0, 0, false
}

func pick(c bool, a, b uint32) uint32 {
	if c {
		return a
	}
	return b
}

func describeInput() string { return "SendInput" }

// Compile-time check: both INPUT variants must be exactly 40 bytes (amd64/arm64).
var (
	_ [40 - unsafe.Sizeof(mouseInput{})]byte
	_ [unsafe.Sizeof(mouseInput{}) - 40]byte
	_ [40 - unsafe.Sizeof(keybdInput{})]byte
	_ [unsafe.Sizeof(keybdInput{}) - 40]byte
)
