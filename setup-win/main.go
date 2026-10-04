// RemoteDesk-Setup.exe: double-click installer for Windows. The relay appends
// the server URL, token and flavour (agent only / agent + app) to the end of
// this exe when it is downloaded, so one binary serves every variant. The
// manifest requests Administrator, so Windows shows the UAC prompt up front.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

const magic = "RDCFG1"

type setupConfig struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	App    bool   `json:"app"`
}

// readTrailer parses [json][magic][uint32 LE json length] at the end of the exe.
func readTrailer() (*setupConfig, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return nil, err
	}
	n := len(data)
	if n < len(magic)+4 || string(data[n-4-len(magic):n-4]) != magic {
		return nil, fmt.Errorf("bộ cài thiếu cấu hình — hãy tải lại từ trang RemoteDesk")
	}
	size := int(binary.LittleEndian.Uint32(data[n-4:]))
	start := n - 4 - len(magic) - size
	if size <= 0 || start < 0 {
		return nil, fmt.Errorf("cấu hình bộ cài bị hỏng")
	}
	var c setupConfig
	if err := json.Unmarshal(bytes.TrimSpace(data[start:start+size]), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func pause() {
	fmt.Println()
	fmt.Print("Nhấn Enter để đóng cửa sổ này...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func main() {
	fmt.Println("==============================================")
	fmt.Println("  RemoteDesk Setup")
	fmt.Println("==============================================")
	c, err := readTrailer()
	if err != nil {
		fmt.Println("LỖI:", err)
		pause()
		os.Exit(1)
	}
	app := "1"
	what := "agent + app RemoteDesk"
	if !c.App {
		app = "0"
		what = "agent (máy này để được điều khiển)"
	}
	fmt.Printf("Cài %s, server %s\n\n", what, c.Server)

	// The PowerShell installer does the actual work (download agent + ffmpeg,
	// config, scheduled task, shortcuts); it is the same script the web page offers.
	script := fmt.Sprintf("[Console]::OutputEncoding=[Text.Encoding]::UTF8; irm '%s/install.ps1?t=%s&app=%s' | iex", c.Server, c.Token, app)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Println()
		fmt.Println("CÀI ĐẶT THẤT BẠI:", err)
		fmt.Println(`Gửi ảnh chụp cửa sổ này (và C:\ProgramData\RemoteDesk\agent.log nếu có) để được hỗ trợ.`)
		pause()
		os.Exit(1)
	}
	pause()
}
