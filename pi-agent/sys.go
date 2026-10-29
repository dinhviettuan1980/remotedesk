package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxOut = 64 * 1024

// run executes a fixed command (never through a shell) and returns its
// combined output, cut to maxOut.
func run(timeout time.Duration, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("không có lệnh %s", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "SYSTEMD_COLORS=0")
	b, err := cmd.CombinedOutput()
	if len(b) > maxOut {
		b = append(b[:maxOut], "\n…(cắt bớt)"...)
	}
	if ctx.Err() != nil {
		err = errors.New("quá thời gian")
	}
	return strings.TrimRight(string(b), "\n"), err
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(string(b), "\x00"))
}

// ---------------------------------------------------------------- overview

func sysInfo() map[string]any {
	m := map[string]any{"agent": version, "arch": runtime.GOARCH, "now": time.Now().Unix()}
	m["host"], _ = os.Hostname()
	m["model"] = readTrim("/proc/device-tree/model")
	m["kernel"] = readTrim("/proc/sys/kernel/osrelease")
	for _, l := range strings.Split(readTrim("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			m["os"] = strings.Trim(v, `"`)
		}
	}
	if f := strings.Fields(readTrim("/proc/uptime")); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		m["uptime"] = int64(up)
	}
	if f := strings.Fields(readTrim("/proc/loadavg")); len(f) >= 3 {
		m["load"] = strings.Join(f[:3], " ")
	}
	m["cpus"] = runtime.NumCPU()
	if t, err := strconv.Atoi(readTrim("/sys/class/thermal/thermal_zone0/temp")); err == nil {
		m["temp"] = float64(t) / 1000
	}
	mem := map[string]int64{}
	for _, l := range strings.Split(readTrim("/proc/meminfo"), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			v, _ := strconv.ParseInt(f[1], 10, 64)
			mem[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	m["memTotal"], m["memAvail"] = mem["MemTotal"], mem["MemAvailable"]
	m["swapTotal"], m["swapFree"] = mem["SwapTotal"], mem["SwapFree"]
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) == nil {
		m["diskTotal"] = int64(st.Blocks) * int64(st.Bsize)
		m["diskFree"] = int64(st.Bavail) * int64(st.Bsize)
	}
	m["ips"] = addresses()
	if ssid, err := run(3*time.Second, "iwgetid", "-r"); err == nil && ssid != "" {
		m["ssid"] = ssid
	}
	m["rssi"] = wifiSignal()
	if out, err := run(3*time.Second, "vcgencmd", "get_throttled"); err == nil {
		if v, ok := strings.CutPrefix(out, "throttled="); ok {
			m["throttled"] = v
		}
	}
	if out, err := run(3*time.Second, "timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil {
		m["ntp"] = out == "yes"
	}
	if out, err := run(5*time.Second, "systemctl", "--failed", "--no-legend", "--plain", "--no-pager"); err == nil && out != "" {
		var failed []string
		for _, l := range strings.Split(out, "\n") {
			if f := strings.Fields(l); len(f) > 0 {
				failed = append(failed, f[0])
			}
		}
		m["failed"] = failed
	}
	return m
}

// addresses: "wlan0 192.168.2.11/24", skipping loopback and link-local IPv6.
func addresses() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ip, ok := a.(*net.IPNet); ok && !ip.IP.IsLinkLocalUnicast() {
				out = append(out, i.Name+" "+a.String())
			}
		}
	}
	return out
}

// wifiSignal: dBm of the first wireless interface, from /proc/net/wireless.
func wifiSignal() any {
	lines := strings.Split(readTrim("/proc/net/wireless"), "\n")
	for _, l := range lines[min(2, len(lines)):] {
		if f := strings.Fields(l); len(f) >= 4 {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f[3], "."), 64); err == nil {
				return v
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- services

type unit struct {
	Name    string `json:"name"`
	Load    string `json:"load"`
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Enabled string `json:"enabled,omitempty"`
	Desc    string `json:"desc"`
	Watch   bool   `json:"watch,omitempty"`
}

func watched(name string, patterns []string) bool {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".service"), ".timer")
	for _, p := range patterns {
		if ok, _ := path.Match(p, base); ok {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func listServices(watch []string) []unit {
	byName := map[string]*unit{}
	out, _ := run(10*time.Second, "systemctl", "list-units", "--type=service,timer", "--all", "--no-legend", "--plain", "--no-pager")
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		byName[f[0]] = &unit{Name: f[0], Load: f[1], Active: f[2], Sub: f[3], Desc: strings.Join(f[4:], " ")}
	}
	files, _ := run(10*time.Second, "systemctl", "list-unit-files", "--type=service,timer", "--no-legend", "--no-pager")
	for _, l := range strings.Split(files, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.Contains(f[0], "@.") { // templates aren't runnable themselves
			continue
		}
		if u := byName[f[0]]; u != nil {
			u.Enabled = f[1]
		} else if watched(f[0], watch) { // installed but not loaded: still worth showing
			byName[f[0]] = &unit{Name: f[0], Load: "loaded", Active: "inactive", Sub: "dead", Enabled: f[1]}
		}
	}
	list := make([]unit, 0, len(byName))
	for _, u := range byName {
		if u.Load == "not-found" {
			continue
		}
		u.Watch = watched(u.Name, watch)
		list = append(list, *u)
	}
	rank := func(u unit) int {
		switch {
		case u.Watch:
			return 0
		case u.Active == "failed":
			return 1
		case u.Active == "active":
			return 2
		}
		return 3
	}
	sort.Slice(list, func(i, j int) bool {
		if ri, rj := rank(list[i]), rank(list[j]); ri != rj {
			return ri < rj
		}
		return list[i].Name < list[j].Name
	})
	return list
}

var unitName = regexp.MustCompile(`^[A-Za-z0-9@._:\-]+\.(service|timer)$`)

// isSelf: this agent's own unit.
func isSelf(u string) bool { return strings.HasPrefix(u, "remotedesk-pi.") }

// protected: stopping or disabling these cuts the only way back in (the agent,
// or the network it reaches the relay over; Linh Địa Việt's Wi-Fi setup and
// network priorities also depend on NetworkManager).
func protected(u string) bool { return isSelf(u) || u == "NetworkManager.service" }

func (a *Agent) serviceAction(vid int, act, u string) {
	reply := func(title, out string, ok bool) {
		a.reply(vid, "pi-out", map[string]any{"title": title, "out": out, "ok": ok})
	}
	if !unitName.MatchString(u) {
		reply("Tên dịch vụ không hợp lệ", u, false)
		return
	}
	var out string
	var err error
	switch act {
	case "status":
		out, err = run(10*time.Second, "systemctl", "status", "--no-pager", "-l", "-n", "30", u)
		if err != nil && out != "" {
			err = nil // non-zero just means "not running"
		}
		reply("systemctl status "+u, out, err == nil)
		return
	case "logs":
		out, err = run(15*time.Second, "journalctl", "-u", u, "-n", "300", "--no-pager", "-o", "short-iso")
		reply("Log "+u+" (300 dòng cuối)", out, err == nil)
		return
	case "restart", "start", "stop", "enable", "disable":
		if protected(u) && (act == "stop" || act == "disable") {
			reply("Không cho phép", "Dừng hoặc tắt "+u+" sẽ làm Pi mất kết nối quản lý từ xa.", false)
			return
		}
		if isSelf(u) && act == "restart" {
			reply("systemctl restart "+u, "Agent đang khởi động lại, sẽ tự nối lại sau vài giây.", true)
			time.Sleep(500 * time.Millisecond)
		}
		out, err = run(60*time.Second, "systemctl", act, u)
		if err != nil {
			out = strings.TrimSpace(out + "\n" + err.Error())
		} else if out == "" {
			out = "OK"
		}
		reply("systemctl "+act+" "+u, out, err == nil)
		a.reply(vid, "pi-svc", map[string]any{"units": listServices(a.cfg.Watch)})
		return
	}
	reply("Lệnh không hợp lệ", act, false)
}

// ---------------------------------------------------------------- hardware

type section struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// hardware: what is plugged in and what is talking on the network. Each part
// degrades to a note when its tool is missing.
func hardware() []section {
	var s []section
	add := func(title string, parts ...string) {
		var b []string
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				b = append(b, p)
			}
		}
		if len(b) == 0 {
			b = []string{"(không có)"}
		}
		s = append(s, section{title, strings.Join(b, "\n\n")})
	}
	try := func(t time.Duration, name string, args ...string) string {
		out, err := run(t, name, args...)
		if err != nil && out == "" {
			return "(" + err.Error() + ")"
		}
		return out
	}

	add("USB", try(5*time.Second, "lsusb"))
	add("Âm thanh (ALSA)", try(5*time.Second, "aplay", "-l"))
	video, _ := run(3*time.Second, "sh", "-c", "ls -1 /dev/video* 2>/dev/null")
	cams := ""
	if _, err := exec.LookPath("rpicam-hello"); err == nil {
		cams = try(15*time.Second, "rpicam-hello", "--list-cameras")
	} else if _, err := exec.LookPath("libcamera-hello"); err == nil {
		cams = try(15*time.Second, "libcamera-hello", "--list-cameras")
	}
	if video != "" {
		video = "Thiết bị video:\n" + video
	}
	add("Camera", cams, video)
	bt := try(5*time.Second, "bluetoothctl", "devices", "Connected")
	if strings.Contains(bt, "Invalid command") || strings.HasPrefix(bt, "(") {
		bt = try(5*time.Second, "bluetoothctl", "paired-devices")
	}
	add("Bluetooth (đang kết nối)", bt)
	ssid, _ := run(3*time.Second, "iwgetid")
	add("Mạng", try(5*time.Second, "ip", "-br", "addr"), try(5*time.Second, "ip", "route"), ssid)
	add("Cổng đang mở (lắng nghe)", try(5*time.Second, "ss", "-tulpnH"))
	add("Kết nối đang mở ra ngoài", try(5*time.Second, "ss", "-tnpH", "state", "established"))
	pw := []string{}
	for _, args := range [][]string{{"get_throttled"}, {"measure_temp"}, {"measure_volts", "core"}} {
		if out, err := run(3*time.Second, "vcgencmd", args...); err == nil {
			pw = append(pw, out)
		}
	}
	if len(pw) > 0 {
		pw = append(pw, "throttled=0x0 là tốt. Bit 0x1/0x10000: thiếu điện áp (nguồn yếu), 0x4/0x40000: đã bị giảm xung.")
	}
	add("Nguồn & nhiệt (vcgencmd)", strings.Join(pw, "\n"))
	return s
}
