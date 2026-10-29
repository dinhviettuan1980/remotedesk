package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var errReplaced = errors.New("replaced")

type Agent struct {
	cfg *Config

	mu     sync.Mutex
	out    chan []byte    // JSON text messages to the relay
	shells map[int]*shell // web terminals by viewer id
}

func newAgent(cfg *Config) *Agent {
	return &Agent{cfg: cfg, shells: map[int]*shell{}}
}

func (a *Agent) wsURL() (string, error) {
	u, err := url.Parse(a.cfg.Server)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws/agent"
	q := url.Values{}
	q.Set("id", a.cfg.ID)
	q.Set("name", a.cfg.Name)
	// "linux" means this kind of agent to the relay and viewer (Pi tab, no
	// screen), whatever it runs on — also a local test build on a Mac.
	q.Set("os", "linux")
	q.Set("ver", version)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// runOnce holds one relay connection until it drops.
func (a *Agent) runOnce() error {
	u, err := a.wsURL()
	if err != nil {
		return err
	}
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, resp, err := d.Dial(u, http.Header{"Authorization": {"Bearer " + a.cfg.Token}})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %v (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial: %v", err)
	}
	defer ws.Close()
	log.Printf("connected to %s as %q", a.cfg.Server, a.cfg.ID)

	out := make(chan []byte, 256)
	a.mu.Lock()
	a.out = out
	a.mu.Unlock()

	// The relay pings every 20 s; treat 60 s of silence as a dead link.
	_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for m := range out {
			_ = ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err := ws.WriteMessage(websocket.TextMessage, m); err != nil {
				log.Printf("write: %v", err)
				ws.Close()
				for range out { // drain so senders never block
				}
				return
			}
		}
	}()

	defer func() {
		a.closeShells()
		a.mu.Lock()
		a.out = nil
		a.mu.Unlock()
		close(out)
		<-writerDone
	}()

	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && ce.Code == 4000 {
				return errReplaced
			}
			return err
		}
		_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		if kind == websocket.TextMessage {
			a.handle(data)
		}
	}
}

func (a *Agent) send(v any) {
	b, _ := json.Marshal(v)
	// Hold the lock while queueing: runOnce closes the channel under it.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.out == nil {
		return
	}
	select {
	case a.out <- b:
	default: // relay link backed up: drop rather than block a reader
	}
}

// reply sends m to one viewer (the relay routes on "to").
func (a *Agent) reply(vid int, t string, m map[string]any) {
	if m == nil {
		m = map[string]any{}
	}
	m["t"] = t
	m["to"] = vid
	a.send(m)
}

type inMsg struct {
	T    string `json:"t"`
	Vid  int    `json:"vid"`  // set by the relay on viewer messages
	Act  string `json:"act"`  // pi-act
	Unit string `json:"unit"` // pi-act
	D    string `json:"d"`    // sh-in: base64 bytes
	Cols int    `json:"cols"` // sh-open / sh-size
	Rows int    `json:"rows"`
}

func (a *Agent) handle(data []byte) {
	var m inMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	switch m.T {
	case "pi-info":
		go a.reply(m.Vid, "pi-info", sysInfo())
	case "pi-svc":
		go a.reply(m.Vid, "pi-svc", map[string]any{"units": listServices(a.cfg.Watch)})
	case "pi-act":
		go a.serviceAction(m.Vid, m.Act, m.Unit)
	case "pi-hw":
		go a.reply(m.Vid, "pi-hw", map[string]any{"sections": hardware()})
	case "sh-open":
		a.openShell(m.Vid, m.Cols, m.Rows)
	case "sh-in":
		a.shellInput(m.Vid, m.D)
	case "sh-size":
		a.shellResize(m.Vid, m.Cols, m.Rows)
	case "sh-close", "rtc-bye": // rtc-bye: the relay says this viewer left
		a.closeShell(m.Vid)
	case "reboot":
		log.Printf("reboot requested by viewer")
		a.send(map[string]any{"t": "notice", "msg": "Đang khởi động lại Pi… Sẽ tự nối lại khi máy lên."})
		go func() {
			time.Sleep(time.Second) // let the notice out
			if out, err := run(30*time.Second, "systemctl", "reboot"); err != nil {
				a.send(map[string]any{"t": "notice", "msg": "Không khởi động lại được: " + err.Error() + " " + out})
			}
		}()
	case "update":
		go func() {
			log.Printf("update requested by viewer")
			a.send(map[string]any{"t": "notice", "msg": "Đang tải bản cập nhật agent…"})
			if err := selfUpdate(a.cfg); err != nil {
				log.Printf("update: %v", err)
				a.send(map[string]any{"t": "notice", "msg": "Không cập nhật được: " + err.Error()})
			}
		}()
	}
	// join / leave / ice / watcher / cfg are for screen sharing: nothing to do here.
}
