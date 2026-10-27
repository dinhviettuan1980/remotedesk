package main

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>

static void rd_mouse(int type, double x, double y, int button, int clicks, unsigned long long flags) {
	CGEventRef e = CGEventCreateMouseEvent(NULL, (CGEventType)type, CGPointMake(x, y), (CGMouseButton)button);
	if (!e) return;
	if (clicks > 0) CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void rd_key(int keycode, int down, unsigned long long flags) {
	CGEventRef e = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, down ? true : false);
	if (!e) return;
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

// rd_text: a key event carrying the characters themselves (keycode ignored).
static void rd_text(const UniChar *chars, int n) {
	for (int down = 1; down >= 0; down--) {
		CGEventRef e = CGEventCreateKeyboardEvent(NULL, 0, down ? true : false);
		if (!e) return;
		CGEventSetFlags(e, 0);
		CGEventKeyboardSetUnicodeString(e, n, chars);
		CGEventPost(kCGHIDEventTap, e);
		CFRelease(e);
	}
}

// CGEventCreateScrollWheelEvent is variadic, which cgo can't call directly.
static void rd_scroll(int dy, int dx) {
	CGEventRef e = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, dy, dx);
	if (!e) return;
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

// rd_focus_edit: 0 = focus is not a text field, 1 = it is and *x,*y hold the
// caret (global coordinates), 2 = it is but the caret position is unknown.
static pid_t rd_front_pid(void) {
	pid_t pid = 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list) && !pid; i++) {
		CFDictionaryRef w = CFArrayGetValueAtIndex(list, i);
		int layer = -1;
		CFNumberRef n = CFDictionaryGetValue(w, kCGWindowLayer);
		if (n) CFNumberGetValue(n, kCFNumberIntType, &layer);
		if (layer != 0) continue; // menu bar, Dock, overlays
		CFNumberRef p = CFDictionaryGetValue(w, kCGWindowOwnerPID);
		if (p) CFNumberGetValue(p, kCFNumberIntType, &pid);
	}
	CFRelease(list);
	return pid;
}

static int rd_focus_edit(double *x, double *y) {
	AXUIElementRef sys = AXUIElementCreateSystemWide();
	AXUIElementSetMessagingTimeout(sys, 0.3); // a hung app must not stall the agent
	AXUIElementRef el = NULL;
	int edit = 0;
	// The system-wide element often answers kAXErrorCannotComplete; asking the
	// app that owns the front window works.
	if (AXUIElementCopyAttributeValue(sys, kAXFocusedUIElementAttribute, (CFTypeRef *)&el) != kAXErrorSuccess || !el) {
		el = NULL;
		pid_t pid = rd_front_pid();
		if (pid > 0) {
			AXUIElementRef app = AXUIElementCreateApplication(pid);
			AXUIElementSetMessagingTimeout(app, 0.3);
			if (AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, (CFTypeRef *)&el) != kAXErrorSuccess) el = NULL;
			CFRelease(app);
		}
	}
	if (el) {
		CFTypeRef role = NULL;
		if (AXUIElementCopyAttributeValue(el, kAXRoleAttribute, &role) == kAXErrorSuccess && role) {
			edit = CFEqual(role, kAXTextFieldRole) || CFEqual(role, kAXTextAreaRole) ||
				CFEqual(role, kAXComboBoxRole) || CFEqual(role, CFSTR("AXSearchField"));
			CFRelease(role);
		}
		if (!edit) { // contenteditable in web pages
			CFTypeRef ed = NULL;
			if (AXUIElementCopyAttributeValue(el, CFSTR("AXEditable"), &ed) == kAXErrorSuccess && ed) {
				edit = CFGetTypeID(ed) == CFBooleanGetTypeID() && CFBooleanGetValue(ed);
				CFRelease(ed);
			}
		}
		if (edit) {
			int got = 0;
			CFTypeRef range = NULL, b = NULL;
			if (AXUIElementCopyAttributeValue(el, kAXSelectedTextRangeAttribute, &range) == kAXErrorSuccess && range) {
				if (AXUIElementCopyParameterizedAttributeValue(el, kAXBoundsForRangeParameterizedAttribute, range, &b) == kAXErrorSuccess && b) {
					CGRect r;
					if (AXValueGetValue(b, kAXValueCGRectType, &r) && r.size.height > 0) {
						*x = r.origin.x; *y = r.origin.y + r.size.height / 2; got = 1;
					}
					CFRelease(b);
				}
				CFRelease(range);
			}
			if (!got) { // the field itself: its left edge, first line
				CFTypeRef p = NULL, s = NULL;
				CGPoint pt; CGSize sz;
				if (AXUIElementCopyAttributeValue(el, kAXPositionAttribute, &p) == kAXErrorSuccess && p &&
					AXUIElementCopyAttributeValue(el, kAXSizeAttribute, &s) == kAXErrorSuccess && s &&
					AXValueGetValue(p, kAXValueCGPointType, &pt) && AXValueGetValue(s, kAXValueCGSizeType, &sz)) {
					*x = pt.x + 8; *y = pt.y + (sz.height > 60 ? 20 : sz.height / 2); got = 1;
				}
				if (p) CFRelease(p);
				if (s) CFRelease(s);
			}
			if (!got) edit = 2;
		}
		CFRelease(el);
	}
	CFRelease(sys);
	return edit;
}

static int rd_screen_access(int prompt) {
	if (CGPreflightScreenCaptureAccess()) return 1;
	if (prompt) CGRequestScreenCaptureAccess();
	return 0;
}

static int rd_ax_access(int prompt) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *vals[] = { prompt ? kCFBooleanTrue : kCFBooleanFalse };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	Boolean ok = AXIsProcessTrustedWithOptions(opts);
	CFRelease(opts);
	return ok ? 1 : 0;
}
*/
import "C"

import (
	"math"
	"os/exec"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	evLeftDown     = 1
	evLeftUp       = 2
	evRightDown    = 3
	evRightUp      = 4
	evMoved        = 5
	evLeftDragged  = 6
	evRightDragged = 7
	evOtherDown    = 25
	evOtherUp      = 26
	evOtherDragged = 27

	flagShift   = 0x20000
	flagControl = 0x40000
	flagOption  = 0x80000
	flagCommand = 0x100000
)

var modifierFlags = map[string]uint64{
	"ShiftLeft": flagShift, "ShiftRight": flagShift,
	"ControlLeft": flagControl, "ControlRight": flagControl,
	"AltLeft": flagOption, "AltRight": flagOption,
	"MetaLeft": flagCommand, "MetaRight": flagCommand,
}

type macInjector struct {
	mu      sync.Mutex
	disp    Display // the captured display: mouse and caret coordinates map onto it
	x, y    float64 // last position in global points
	buttons map[int]bool
	keys    map[string]bool
	// double-click detection
	lastBtn   int
	lastClick time.Time
	lastPos   [2]float64
	clicks    int
	wheelAccX float64
	wheelAccY float64
}

func newInjector() Injector {
	return &macInjector{buttons: map[int]bool{}, keys: map[string]bool{}}
}

func (m *macInjector) SetDisplay(d Display) {
	m.mu.Lock()
	m.disp = d
	m.mu.Unlock()
}

func (m *macInjector) flags() uint64 {
	var f uint64
	for k := range m.keys {
		f |= modifierFlags[k]
	}
	return f
}

func (m *macInjector) MouseMove(nx, ny float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.x = m.disp.X + nx*(m.disp.W-1)
	m.y = m.disp.Y + ny*(m.disp.H-1)
	typ, btn := evMoved, 0
	switch {
	case m.buttons[0]:
		typ, btn = evLeftDragged, 0
	case m.buttons[2]:
		typ, btn = evRightDragged, 1
	case m.buttons[1]:
		typ, btn = evOtherDragged, 2
	}
	C.rd_mouse(C.int(typ), C.double(m.x), C.double(m.y), C.int(btn), 0, C.ulonglong(m.flags()))
}

// MouseButton uses browser numbering: 0 left, 1 middle, 2 right, 3 back, 4 forward.
func (m *macInjector) MouseButton(b int, down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b < 0 || b > 4 {
		return
	}
	if down {
		now := time.Now()
		near := math.Abs(m.x-m.lastPos[0]) < 5 && math.Abs(m.y-m.lastPos[1]) < 5
		if b == m.lastBtn && near && now.Sub(m.lastClick) < 500*time.Millisecond {
			m.clicks++
		} else {
			m.clicks = 1
		}
		m.lastBtn, m.lastClick, m.lastPos = b, now, [2]float64{m.x, m.y}
	}
	m.buttons[b] = down
	var typ, cgBtn int
	switch b {
	case 0:
		typ, cgBtn = map[bool]int{true: evLeftDown, false: evLeftUp}[down], 0
	case 2:
		typ, cgBtn = map[bool]int{true: evRightDown, false: evRightUp}[down], 1
	default: // middle=2, back=3, forward=4 in CoreGraphics numbering
		cg := map[int]int{1: 2, 3: 3, 4: 4}[b]
		typ, cgBtn = map[bool]int{true: evOtherDown, false: evOtherUp}[down], cg
	}
	C.rd_mouse(C.int(typ), C.double(m.x), C.double(m.y), C.int(cgBtn), C.int(m.clicks), C.ulonglong(m.flags()))
}

func (m *macInjector) Wheel(dx, dy float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// CoreGraphics: positive = content moves down/right (opposite of browser deltas).
	m.wheelAccX -= dx
	m.wheelAccY -= dy
	ix, iy := int(m.wheelAccX), int(m.wheelAccY)
	if ix == 0 && iy == 0 {
		return
	}
	m.wheelAccX -= float64(ix)
	m.wheelAccY -= float64(iy)
	C.rd_scroll(C.int(iy), C.int(ix))
}

func (m *macInjector) Key(code string, down bool) {
	kc, ok := macKeycodes[code]
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if down {
		m.keys[code] = true
	} else {
		delete(m.keys, code)
	}
	C.rd_key(C.int(kc), boolInt(down), C.ulonglong(m.flags()))
}

func (m *macInjector) Text(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range s { // one character per event: some apps read only the first
		u := utf16.Encode([]rune{r})
		C.rd_text((*C.UniChar)(unsafe.Pointer(&u[0])), C.int(len(u)))
	}
}

func (m *macInjector) ReleaseAll() {
	m.mu.Lock()
	keys := make([]string, 0, len(m.keys))
	for k := range m.keys {
		keys = append(keys, k)
	}
	buttons := make([]int, 0, len(m.buttons))
	for b, d := range m.buttons {
		if d {
			buttons = append(buttons, b)
		}
	}
	m.mu.Unlock()
	for _, k := range keys {
		m.Key(k, false)
	}
	for _, b := range buttons {
		m.MouseButton(b, false)
	}
}

func (m *macInjector) Wake() {
	// -u declares user activity, which turns the display on.
	go func() { _ = exec.Command("caffeinate", "-u", "-t", "2").Run() }()
}

func (m *macInjector) FocusedEdit() (bool, float64, float64, bool) {
	var x, y C.double
	switch C.rd_focus_edit(&x, &y) {
	case 0:
		return false, 0, 0, false
	case 2:
		return true, 0, 0, false
	}
	m.mu.Lock()
	d := m.disp
	m.mu.Unlock()
	nx, ny, ok := d.toDisplay(float64(x), float64(y))
	if !ok {
		return true, 0, 0, false // caret on another display
	}
	return true, nx, ny, true
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func screenAccess(prompt bool) bool { return C.rd_screen_access(boolInt(prompt)) == 1 }
func axAccess(prompt bool) bool     { return C.rd_ax_access(boolInt(prompt)) == 1 }

func describeInput() string {
	return "screen-recording=" + yesNo(screenAccess(false)) + " accessibility=" + yesNo(axAccess(false))
}

func yesNo(b bool) string {
	if b {
		return "granted"
	}
	return "MISSING"
}
