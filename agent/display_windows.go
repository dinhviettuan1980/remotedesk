package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procEnumDisplayMonitors    = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procGetDisplayConfigBufSz  = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig     = user32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDevice = user32.NewProc("DisplayConfigGetDeviceInfo")

	dxgi                    = syscall.NewLazyDLL("dxgi.dll")
	procCreateDXGIFactory1  = dxgi.NewProc("CreateDXGIFactory1")
	iidIDXGIFactory1        = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	enumMonitorsCallback    = syscall.NewCallback(enumMonitorProc)
	enumMonitorsMu          sync.Mutex
	enumMonitorsDestination *[]monitorInfoEx
)

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

// MONITORINFOEXW
type monitorInfoEx struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	flags     uint32
	device    [32]uint16
}

const monitorInfoFPrimary = 1

func enumMonitorProc(hmon, hdc, rect, data uintptr) uintptr {
	mi := monitorInfoEx{}
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 && enumMonitorsDestination != nil {
		*enumMonitorsDestination = append(*enumMonitorsDestination, mi)
	}
	return 1
}

// listDisplays returns the monitors (primary first) in virtual-desktop pixels.
func listDisplays() []Display {
	var mons []monitorInfoEx
	enumMonitorsMu.Lock()
	enumMonitorsDestination = &mons // EnumDisplayMonitors calls back synchronously
	procEnumDisplayMonitors.Call(0, 0, enumMonitorsCallback, 0)
	enumMonitorsDestination = nil
	enumMonitorsMu.Unlock()

	names := monitorNames()
	outputs := dxgiOutputs()
	var vl, vt, vr, vb int32
	for i, m := range mons {
		r := m.rcMonitor
		if i == 0 || r.left < vl {
			vl = r.left
		}
		if i == 0 || r.top < vt {
			vt = r.top
		}
		if i == 0 || r.right > vr {
			vr = r.right
		}
		if i == 0 || r.bottom > vb {
			vb = r.bottom
		}
	}
	out := make([]Display, 0, len(mons))
	for i, m := range mons {
		dev := syscall.UTF16ToString(m.device[:])
		r := m.rcMonitor
		w, h := int(r.right-r.left), int(r.bottom-r.top)
		name := names[dev]
		if name == "" {
			name = fmt.Sprintf("Màn hình %d", i+1)
		}
		capIdx := -1
		if idx, ok := outputs[strings.ToUpper(dev)]; ok {
			capIdx = idx
		} else if len(outputs) == 0 && m.flags&monitorInfoFPrimary != 0 {
			capIdx = 0 // DXGI unavailable: what ddagrab did before multi-monitor support
		}
		out = append(out, Display{
			Name: name, X: float64(r.left), Y: float64(r.top), W: float64(w), H: float64(h),
			PxW: w, PxH: h, Primary: m.flags&monitorInfoFPrimary != 0, capIdx: capIdx,
			vx: float64(vl), vy: float64(vt), vw: float64(vr - vl), vh: float64(vb - vt),
		})
	}
	return primaryFirst(out)
}

// ---------------------------------------------------------------- friendly names

// DISPLAYCONFIG_PATH_INFO (72 bytes); LUIDs as two uint32 to keep 4-byte alignment.
type dcPathInfo struct {
	srcAdapterLo, srcAdapterHi uint32
	srcID                      uint32
	srcModeIdx                 uint32
	srcFlags                   uint32
	tgtAdapterLo, tgtAdapterHi uint32
	tgtID                      uint32
	tgtModeIdx                 uint32
	tgtTech, tgtRot, tgtScale  uint32
	tgtRefreshN, tgtRefreshD   uint32
	tgtScanline, tgtAvail      uint32
	tgtFlags                   uint32
	flags                      uint32
}

type dcHeader struct {
	typ, size                uint32
	adapterLo, adapterHi, id uint32
}

type dcSourceName struct {
	hdr     dcHeader
	gdiName [32]uint16
}

type dcTargetName struct {
	hdr                  dcHeader
	flags, tech          uint32
	edidMfg, edidProduct uint16
	connector            uint32
	friendly             [64]uint16
	path                 [128]uint16
}

// monitorNames maps GDI device names (\\.\DISPLAY1) to monitor names from EDID ("DELL U2720Q").
func monitorNames() map[string]string {
	out := map[string]string{}
	const qdcOnlyActivePaths = 2
	var np, nm uint32
	if r, _, _ := procGetDisplayConfigBufSz.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 || np == 0 {
		return out
	}
	paths := make([]dcPathInfo, np)
	modes := make([]byte, 64*int(nm)+64) // DISPLAYCONFIG_MODE_INFO is 64 bytes; contents unused
	if r, _, _ := procQueryDisplayConfig.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return out
	}
	for _, p := range paths[:np] {
		src := dcSourceName{hdr: dcHeader{typ: 1, adapterLo: p.srcAdapterLo, adapterHi: p.srcAdapterHi, id: p.srcID}}
		src.hdr.size = uint32(unsafe.Sizeof(src))
		if r, _, _ := procDisplayConfigGetDevice.Call(uintptr(unsafe.Pointer(&src))); r != 0 {
			continue
		}
		tgt := dcTargetName{hdr: dcHeader{typ: 2, adapterLo: p.tgtAdapterLo, adapterHi: p.tgtAdapterHi, id: p.tgtID}}
		tgt.hdr.size = uint32(unsafe.Sizeof(tgt))
		if r, _, _ := procDisplayConfigGetDevice.Call(uintptr(unsafe.Pointer(&tgt))); r != 0 {
			continue
		}
		name := syscall.UTF16ToString(tgt.friendly[:])
		if name == "" && tgt.tech == 0x80000000 { // DISPLAYCONFIG_OUTPUT_TECHNOLOGY_INTERNAL
			name = "Màn hình tích hợp"
		}
		if name != "" {
			out[syscall.UTF16ToString(src.gdiName[:])] = name
		}
	}
	return out
}

// ---------------------------------------------------------------- DXGI outputs

// dxgiOutputs maps GDI device names to output indices on adapter 0, which is
// what ffmpeg's ddagrab output_idx counts (it opens the default D3D11 device).
func dxgiOutputs() map[string]int {
	out := map[string]int{}
	var factory uintptr
	if r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); int32(r) < 0 || factory == 0 {
		return out
	}
	defer comRelease(factory)
	var adapter uintptr
	if r := comCall(factory, 7, 0, uintptr(unsafe.Pointer(&adapter))); int32(r) < 0 || adapter == 0 { // IDXGIFactory::EnumAdapters
		return out
	}
	defer comRelease(adapter)
	for i := 0; i < 16; i++ {
		var output uintptr
		if r := comCall(adapter, 7, uintptr(i), uintptr(unsafe.Pointer(&output))); int32(r) < 0 || output == 0 { // IDXGIAdapter::EnumOutputs
			break
		}
		// DXGI_OUTPUT_DESC: DeviceName[32], DesktopCoordinates, AttachedToDesktop, Rotation, Monitor.
		var desc struct {
			device   [32]uint16
			coords   winRect
			attached int32
			rotation uint32
			monitor  uintptr
		}
		if r := comCall(output, 7, uintptr(unsafe.Pointer(&desc))); int32(r) >= 0 { // IDXGIOutput::GetDesc
			out[strings.ToUpper(syscall.UTF16ToString(desc.device[:]))] = i
		}
		comRelease(output)
	}
	return out
}

// ---------------------------------------------------------------- COM helpers

// comCall calls method idx of a COM object's vtable. Pointer arguments are
// passed as uintptr(unsafe.Pointer(…)); uintptrescapes keeps them valid.
//
//go:uintptrescapes
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}
