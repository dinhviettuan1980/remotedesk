package main

// Multiple monitors: the agent captures one display at a time. The viewer gets
// the list in "info" ({"screens":[…],"screen":i}) and picks with
// {"t":"screen","i":N}; capture, mouse coordinates and the caret position
// (FocusedEdit) all follow the chosen display.

// Display is one monitor. X/Y/W/H are its bounds in the coordinate space the
// input injector uses: global points on macOS, virtual-desktop pixels on
// Windows (the agent is per-monitor DPI aware there).
type Display struct {
	Name       string
	X, Y, W, H float64
	PxW, PxH   int // native pixels
	Primary    bool

	// macOS: index in CGGetActiveDisplayList (= avfoundation "Capture screen N").
	// Windows: DXGI output index on the default adapter (ddagrab), -1 if none.
	capIdx int
	// Windows: bounds of the whole virtual desktop (MOUSEEVENTF_VIRTUALDESK).
	vx, vy, vw, vh float64
}

type screenInfo struct {
	Name    string `json:"name"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Primary bool   `json:"primary,omitempty"`
}

func screenInfos(list []Display) []screenInfo {
	out := make([]screenInfo, len(list))
	for i, d := range list {
		out[i] = screenInfo{Name: d.Name, W: d.PxW, H: d.PxH, Primary: d.Primary}
	}
	return out
}

// pickDisplay returns display i of list, or the primary one (index 0: the
// lists are sorted primary first) when i is out of range.
func pickDisplay(list []Display, i int) (Display, int) {
	if i < 0 || i >= len(list) {
		i = 0
	}
	if len(list) == 0 {
		return Display{}, 0
	}
	return list[i], i
}

// primaryFirst moves the primary display to the front, keeping the rest in order.
func primaryFirst(list []Display) []Display {
	for i, d := range list {
		if d.Primary && i > 0 {
			out := append([]Display{d}, list[:i]...)
			return append(out, list[i+1:]...)
		}
	}
	return list
}

// toDisplay maps a point in injector coordinates to 0..1 over d; ok is false
// when the point lies on another display.
func (d Display) toDisplay(x, y float64) (nx, ny float64, ok bool) {
	if d.W <= 0 || d.H <= 0 {
		return 0, 0, false
	}
	nx, ny = (x-d.X)/d.W, (y-d.Y)/d.H
	return nx, ny, nx >= 0 && nx <= 1 && ny >= 0 && ny <= 1
}
