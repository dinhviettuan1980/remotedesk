package main

// Keys arrive as browser KeyboardEvent.code values (physical key position),
// so the remote OS's own layout / IME (e.g. Vietnamese Telex) does the typing.

// Injector is implemented per OS (input_darwin.go, input_windows.go).
type Injector interface {
	MouseMove(x, y float64) // normalized 0..1 over the captured display
	MouseButton(btn int, down bool)
	Wheel(dx, dy float64) // browser pixels; positive dy = scroll down
	Key(code string, down bool)
	ReleaseAll()
	Wake() // nudge the display awake
	// FocusedEdit: is the keyboard focus in a text field, and where is its caret
	// (normalized over the captured display; pos false when unknown).
	FocusedEdit() (edit bool, x, y float64, pos bool)
}

// macKeycodes: KeyboardEvent.code → macOS virtual keycode (kVK_*).
var macKeycodes = map[string]int{
	"KeyA": 0x00, "KeyS": 0x01, "KeyD": 0x02, "KeyF": 0x03, "KeyH": 0x04, "KeyG": 0x05, "KeyZ": 0x06,
	"KeyX": 0x07, "KeyC": 0x08, "KeyV": 0x09, "IntlBackslash": 0x0A, "KeyB": 0x0B, "KeyQ": 0x0C,
	"KeyW": 0x0D, "KeyE": 0x0E, "KeyR": 0x0F, "KeyY": 0x10, "KeyT": 0x11, "Digit1": 0x12, "Digit2": 0x13,
	"Digit3": 0x14, "Digit4": 0x15, "Digit6": 0x16, "Digit5": 0x17, "Equal": 0x18, "Digit9": 0x19,
	"Digit7": 0x1A, "Minus": 0x1B, "Digit8": 0x1C, "Digit0": 0x1D, "BracketRight": 0x1E, "KeyO": 0x1F,
	"KeyU": 0x20, "BracketLeft": 0x21, "KeyI": 0x22, "KeyP": 0x23, "Enter": 0x24, "KeyL": 0x25,
	"KeyJ": 0x26, "Quote": 0x27, "KeyK": 0x28, "Semicolon": 0x29, "Backslash": 0x2A, "Comma": 0x2B,
	"Slash": 0x2C, "KeyN": 0x2D, "KeyM": 0x2E, "Period": 0x2F, "Tab": 0x30, "Space": 0x31,
	"Backquote": 0x32, "Backspace": 0x33, "Escape": 0x35, "MetaRight": 0x36, "MetaLeft": 0x37,
	"ShiftLeft": 0x38, "CapsLock": 0x39, "AltLeft": 0x3A, "ControlLeft": 0x3B, "ShiftRight": 0x3C,
	"AltRight": 0x3D, "ControlRight": 0x3E, "Fn": 0x3F, "F17": 0x40, "NumpadDecimal": 0x41,
	"NumpadMultiply": 0x43, "NumpadAdd": 0x45, "NumLock": 0x47, "AudioVolumeUp": 0x48,
	"AudioVolumeDown": 0x49, "AudioVolumeMute": 0x4A, "NumpadDivide": 0x4B, "NumpadEnter": 0x4C,
	"NumpadSubtract": 0x4E, "F18": 0x4F, "F19": 0x50, "NumpadEqual": 0x51, "Numpad0": 0x52,
	"Numpad1": 0x53, "Numpad2": 0x54, "Numpad3": 0x55, "Numpad4": 0x56, "Numpad5": 0x57,
	"Numpad6": 0x58, "Numpad7": 0x59, "F20": 0x5A, "Numpad8": 0x5B, "Numpad9": 0x5C, "IntlYen": 0x5D,
	"IntlRo": 0x5E, "NumpadComma": 0x5F, "F5": 0x60, "F6": 0x61, "F7": 0x62, "F3": 0x63, "F8": 0x64,
	"F9": 0x65, "F11": 0x67, "F13": 0x69, "PrintScreen": 0x69, "F16": 0x6A, "F14": 0x6B, "F10": 0x6D,
	"ContextMenu": 0x6E, "F12": 0x6F, "F15": 0x71, "Insert": 0x72, "Help": 0x72, "Home": 0x73,
	"PageUp": 0x74, "Delete": 0x75, "F4": 0x76, "End": 0x77, "F2": 0x78, "PageDown": 0x79, "F1": 0x7A,
	"ArrowLeft": 0x7B, "ArrowRight": 0x7C, "ArrowDown": 0x7D, "ArrowUp": 0x7E,
}

// winScan: KeyboardEvent.code → PC set-1 scancode; ext marks E0-prefixed keys.
type winKey struct {
	scan uint16
	ext  bool
	vk   uint16 // used instead of scan when non-zero (keys without a usable scancode)
}

var winKeys = map[string]winKey{
	"Escape": {0x01, false, 0}, "Digit1": {0x02, false, 0}, "Digit2": {0x03, false, 0}, "Digit3": {0x04, false, 0},
	"Digit4": {0x05, false, 0}, "Digit5": {0x06, false, 0}, "Digit6": {0x07, false, 0}, "Digit7": {0x08, false, 0},
	"Digit8": {0x09, false, 0}, "Digit9": {0x0A, false, 0}, "Digit0": {0x0B, false, 0}, "Minus": {0x0C, false, 0},
	"Equal": {0x0D, false, 0}, "Backspace": {0x0E, false, 0}, "Tab": {0x0F, false, 0}, "KeyQ": {0x10, false, 0},
	"KeyW": {0x11, false, 0}, "KeyE": {0x12, false, 0}, "KeyR": {0x13, false, 0}, "KeyT": {0x14, false, 0},
	"KeyY": {0x15, false, 0}, "KeyU": {0x16, false, 0}, "KeyI": {0x17, false, 0}, "KeyO": {0x18, false, 0},
	"KeyP": {0x19, false, 0}, "BracketLeft": {0x1A, false, 0}, "BracketRight": {0x1B, false, 0},
	"Enter": {0x1C, false, 0}, "ControlLeft": {0x1D, false, 0}, "KeyA": {0x1E, false, 0}, "KeyS": {0x1F, false, 0},
	"KeyD": {0x20, false, 0}, "KeyF": {0x21, false, 0}, "KeyG": {0x22, false, 0}, "KeyH": {0x23, false, 0},
	"KeyJ": {0x24, false, 0}, "KeyK": {0x25, false, 0}, "KeyL": {0x26, false, 0}, "Semicolon": {0x27, false, 0},
	"Quote": {0x28, false, 0}, "Backquote": {0x29, false, 0}, "ShiftLeft": {0x2A, false, 0},
	"Backslash": {0x2B, false, 0}, "KeyZ": {0x2C, false, 0}, "KeyX": {0x2D, false, 0}, "KeyC": {0x2E, false, 0},
	"KeyV": {0x2F, false, 0}, "KeyB": {0x30, false, 0}, "KeyN": {0x31, false, 0}, "KeyM": {0x32, false, 0},
	"Comma": {0x33, false, 0}, "Period": {0x34, false, 0}, "Slash": {0x35, false, 0}, "ShiftRight": {0x36, false, 0},
	"NumpadMultiply": {0x37, false, 0}, "AltLeft": {0x38, false, 0}, "Space": {0x39, false, 0},
	"CapsLock": {0x3A, false, 0}, "F1": {0x3B, false, 0}, "F2": {0x3C, false, 0}, "F3": {0x3D, false, 0},
	"F4": {0x3E, false, 0}, "F5": {0x3F, false, 0}, "F6": {0x40, false, 0}, "F7": {0x41, false, 0},
	"F8": {0x42, false, 0}, "F9": {0x43, false, 0}, "F10": {0x44, false, 0}, "NumLock": {0x45, true, 0},
	"ScrollLock": {0x46, false, 0}, "Numpad7": {0x47, false, 0}, "Numpad8": {0x48, false, 0},
	"Numpad9": {0x49, false, 0}, "NumpadSubtract": {0x4A, false, 0}, "Numpad4": {0x4B, false, 0},
	"Numpad5": {0x4C, false, 0}, "Numpad6": {0x4D, false, 0}, "NumpadAdd": {0x4E, false, 0},
	"Numpad1": {0x4F, false, 0}, "Numpad2": {0x50, false, 0}, "Numpad3": {0x51, false, 0},
	"Numpad0": {0x52, false, 0}, "NumpadDecimal": {0x53, false, 0}, "IntlBackslash": {0x56, false, 0},
	"F11": {0x57, false, 0}, "F12": {0x58, false, 0}, "NumpadEqual": {0x59, false, 0},
	"F13": {0x64, false, 0}, "F14": {0x65, false, 0}, "F15": {0x66, false, 0}, "F16": {0x67, false, 0},
	"F17": {0x68, false, 0}, "F18": {0x69, false, 0}, "F19": {0x6A, false, 0}, "F20": {0x6B, false, 0},
	"F21": {0x6C, false, 0}, "F22": {0x6D, false, 0}, "F23": {0x6E, false, 0}, "F24": {0x76, false, 0},
	"IntlRo": {0x73, false, 0}, "IntlYen": {0x7D, false, 0}, "NumpadComma": {0x7E, false, 0},
	"NumpadEnter": {0x1C, true, 0}, "ControlRight": {0x1D, true, 0}, "NumpadDivide": {0x35, true, 0},
	"PrintScreen": {0x37, true, 0}, "AltRight": {0x38, true, 0}, "Home": {0x47, true, 0},
	"ArrowUp": {0x48, true, 0}, "PageUp": {0x49, true, 0}, "ArrowLeft": {0x4B, true, 0},
	"ArrowRight": {0x4D, true, 0}, "End": {0x4F, true, 0}, "ArrowDown": {0x50, true, 0},
	"PageDown": {0x51, true, 0}, "Insert": {0x52, true, 0}, "Delete": {0x53, true, 0},
	"MetaLeft": {0x5B, true, 0}, "MetaRight": {0x5C, true, 0}, "ContextMenu": {0x5D, true, 0},
	"AudioVolumeMute": {0x20, true, 0}, "AudioVolumeDown": {0x2E, true, 0}, "AudioVolumeUp": {0x30, true, 0},
	"MediaPlayPause": {0x22, true, 0}, "MediaStop": {0x24, true, 0}, "MediaTrackNext": {0x19, true, 0},
	"MediaTrackPrevious": {0x10, true, 0}, "Pause": {0, false, 0x13},
}
