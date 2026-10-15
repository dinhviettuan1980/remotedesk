package main

/*
#cgo LDFLAGS: -framework AppKit
#include <ApplicationServices/ApplicationServices.h>

int rd_display_name(unsigned int did, char *buf, int len);

typedef struct { unsigned int id; double x, y, w, h; int pxw, pxh, main, builtin; } rd_disp;

// rd_displays fills out in CGGetActiveDisplayList order, which is also
// avfoundation's "Capture screen N" order.
static int rd_displays(rd_disp *out, int max) {
	CGDirectDisplayID ids[16];
	uint32_t n = 0;
	if (CGGetActiveDisplayList(16, ids, &n) != kCGErrorSuccess) return 0;
	if ((int)n > max) n = max;
	for (uint32_t i = 0; i < n; i++) {
		CGRect r = CGDisplayBounds(ids[i]);
		out[i].id = ids[i];
		out[i].x = r.origin.x; out[i].y = r.origin.y; out[i].w = r.size.width; out[i].h = r.size.height;
		out[i].pxw = (int)r.size.width; out[i].pxh = (int)r.size.height;
		CGDisplayModeRef m = CGDisplayCopyDisplayMode(ids[i]);
		if (m) {
			out[i].pxw = (int)CGDisplayModeGetPixelWidth(m);
			out[i].pxh = (int)CGDisplayModeGetPixelHeight(m);
			CGDisplayModeRelease(m);
		}
		out[i].main = CGDisplayIsMain(ids[i]) ? 1 : 0;
		out[i].builtin = CGDisplayIsBuiltin(ids[i]) ? 1 : 0;
	}
	return (int)n;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// listDisplays returns the active displays, main display first.
func listDisplays() []Display {
	var buf [16]C.rd_disp
	n := int(C.rd_displays(&buf[0], 16))
	out := make([]Display, 0, n)
	ext := 0
	for i := 0; i < n; i++ {
		d := buf[i]
		var name [128]C.char
		var label string
		if C.rd_display_name(d.id, &name[0], C.int(len(name))) == 1 {
			label = C.GoString((*C.char)(unsafe.Pointer(&name[0])))
		} else if d.builtin == 1 {
			label = "Màn hình tích hợp"
		} else {
			ext++
			label = fmt.Sprintf("Màn hình ngoài %d", ext)
		}
		out = append(out, Display{
			Name: label, X: float64(d.x), Y: float64(d.y), W: float64(d.w), H: float64(d.h),
			PxW: int(d.pxw), PxH: int(d.pxh), Primary: d.main == 1, capIdx: i,
		})
	}
	return primaryFirst(out)
}
