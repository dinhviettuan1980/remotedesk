package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

var errReplaced = errors.New("replaced")

type outMsg struct {
	kind int // websocket.TextMessage / BinaryMessage
	data []byte
}

type Agent struct {
	cfg   *Config
	ff    string
	input Injector
	inMu  sync.Mutex // input arrives from the relay and from P2P data channels

	mu       sync.Mutex
	out      chan outMsg
	sess     *Session
	params   StreamParams
	viewer   bool
	dropping bool      // video backpressure: skip frames until the next keyframe
	wsVideo  bool      // relay wants video (false while every viewer gets it P2P)
	lastKf   time.Time // last ffmpeg (re)start
	lastKey  time.Time // last keyframe out of the encoder
	peers    map[int]*peer
	files    fileXfer
	ice      []webrtc.ICEServer // from the relay: STUN + short-lived TURN credentials
	t0       time.Time
	clip     *clipSync
	awake    func() // stops keep-awake
	screen   int    // display to capture (index into listDisplays; the viewer's 🖥 choice)

	audio     *audioSession // running while some viewer wants sound (audio.go)
	audioVids map[int]bool  // viewers that want sound
	wsAudio   bool          // some of them get it over the relay (the rest via P2P)

	editMu    sync.Mutex // text-field check after clicks / keys (checkEdit)
	editTimer *time.Timer
	editClick bool
}

func newAgent(cfg *Config, ff string) *Agent {
	go audioAvailable(ff) // probe the Opus encoder now, not when the first viewer joins
	return &Agent{cfg: cfg, ff: ff, input: newInjector(), screen: cfg.Display, t0: time.Now()}
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
	q.Set("os", runtime.GOOS)
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
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment, WriteBufferSize: 256 * 1024}
	ws, resp, err := d.Dial(u, http.Header{"Authorization": {"Bearer " + a.cfg.Token}})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %v (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial: %v", err)
	}
	defer ws.Close()
	log.Printf("connected to %s as %q", a.cfg.Server, a.cfg.ID)

	out := make(chan outMsg, 256)
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
			if err := ws.WriteMessage(m.kind, m.data); err != nil {
				log.Printf("write: %v", err)
				ws.Close()
				for range out { // drain so senders never block
				}
				return
			}
		}
	}()

	defer func() {
		a.leave()
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
		} else if len(data) > 0 && data[0] == 2 {
			a.onFileChunk(data)
		}
	}
}

// ---------------------------------------------------------------- outbound

func (a *Agent) sendJSON(v any) {
	b, _ := json.Marshal(v)
	// Hold the lock while queueing: runOnce closes the channel under it.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.out == nil {
		return
	}
	select {
	case a.out <- outMsg{websocket.TextMessage, b}:
	default:
		log.Printf("send queue full, dropped control message")
	}
}

// sendFrame hands one access unit to the P2P peers and queues it for the
// relay. If the uplink can't keep up, frames are dropped until the next
// keyframe so the viewer never decodes a broken chain.
func (a *Agent) sendFrame(au []byte, key bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if key {
		a.lastKey = now
	}
	if !a.viewer {
		return
	}
	a.sendP2P(au, key, now)
	if a.out == nil || !a.wsVideo {
		return
	}
	if a.dropping {
		if !key || len(a.out) > 4 {
			return
		}
		a.dropping = false
	}
	if len(a.out) > 30 {
		a.dropping = true
		log.Printf("uplink congested: dropping until next keyframe")
		return
	}
	msg := make([]byte, 6+len(au))
	msg[0] = 1
	if key {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[2:], uint32(time.Since(a.t0).Milliseconds()))
	copy(msg[6:], au)
	a.out <- outMsg{websocket.BinaryMessage, msg}
}

// sendAudio hands one Opus packet to the P2P peers that want sound and, if
// some listener is on the relay, queues it there (binary type 3).
func (a *Agent) sendAudio(pkt []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.viewer {
		return
	}
	for _, p := range a.peers {
		if p.connected && !p.closed && p.audioFrames != nil && a.audioVids[p.vid] {
			select {
			case p.audioFrames <- pkt:
			default:
			}
		}
	}
	// Video has priority on a congested uplink; a lost 20 ms of sound is a click.
	if a.out == nil || !a.wsAudio || len(a.out) > 30 {
		return
	}
	msg := make([]byte, 6+len(pkt))
	msg[0] = 3
	binary.BigEndian.PutUint32(msg[2:], uint32(time.Since(a.t0).Milliseconds()))
	copy(msg[6:], pkt)
	a.out <- outMsg{websocket.BinaryMessage, msg}
}

// ---------------------------------------------------------------- inbound

type inMsg struct {
	T    string  `json:"t"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	B    int     `json:"b"`
	DX   float64 `json:"dx"`
	DY   float64 `json:"dy"`
	C    string  `json:"c"`
	Text string  `json:"text"`
	StreamParams
	Vid  int                        `json:"vid"`
	SDP  *webrtc.SessionDescription `json:"sdp"`
	Cand json.RawMessage            `json:"cand"`
	On   bool                       `json:"on"`
	// file transfer (files.go)
	ID    int      `json:"id"`
	Path  string   `json:"path"`
	Name  string   `json:"name"`
	Size  int64    `json:"size"`
	Got   int64    `json:"got"`
	Clip  bool     `json:"clip"`
	Batch string   `json:"batch"`
	Paths []string `json:"paths"`
	// relay → agent
	Servers []webrtc.ICEServer `json:"servers"`
	Vids    []int              `json:"vids"` // "audio": viewers that want sound
	WS      bool               `json:"ws"`   // "audio": some of them are on the relay
	// viewer → agent
	I int `json:"i"` // "screen": display index
}

// handleInput is the entry point for the P2P data channel: input only, no
// session control (the relay must see join/cfg/leave).
func (a *Agent) handleInput(data []byte) {
	var m inMsg
	if json.Unmarshal(data, &m) == nil {
		a.input1(&m)
	}
}

// input1 applies one input message; false if m is not an input message.
func (a *Agent) input1(m *inMsg) bool {
	a.inMu.Lock()
	defer a.inMu.Unlock()
	switch m.T {
	case "mm":
		a.input.MouseMove(clamp01(m.X), clamp01(m.Y))
	case "md":
		a.input.MouseButton(m.B, true)
	case "mu":
		a.input.MouseButton(m.B, false)
		if m.B == 0 {
			a.scheduleEditCheck(true)
		}
	case "wh":
		a.input.Wheel(m.DX, m.DY)
	case "kd":
		a.input.Key(m.C, true)
	case "ku":
		a.input.Key(m.C, false)
		a.scheduleEditCheck(false)
	case "ra":
		a.input.ReleaseAll()
	case "clip":
		a.mu.Lock()
		clip := a.clip
		a.mu.Unlock()
		if clip != nil && len(m.Text) <= 1<<20 {
			clip.setRemote(m.Text)
		}
	default:
		return false
	}
	return true
}

// Phone viewers open their keyboard when a click lands in a text field, and
// keep the caret in view while typing: after a left click (and after typing
// pauses) tell the viewers whether the focus is a text field and where.
func (a *Agent) scheduleEditCheck(click bool) {
	a.editMu.Lock()
	defer a.editMu.Unlock()
	if click {
		a.editClick = true
	}
	if a.editTimer != nil {
		a.editTimer.Stop()
	}
	a.editTimer = time.AfterFunc(150*time.Millisecond, a.checkEdit)
}

func (a *Agent) checkEdit() {
	a.editMu.Lock()
	click := a.editClick
	a.editClick = false
	a.editMu.Unlock()
	edit, x, y, pos := a.input.FocusedEdit()
	if !click && !(edit && pos) {
		return // typing: only caret moves are news
	}
	msg := map[string]any{"t": "edit", "on": edit, "click": click}
	if pos {
		msg["x"], msg["y"] = x, y
	}
	a.sendJSON(msg)
}

func (a *Agent) handle(data []byte) {
	var m inMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	if a.input1(&m) {
		return
	}
	switch m.T {
	case "join":
		a.join(m.StreamParams.clamp())
	case "leave":
		a.leave()
	case "cfg":
		a.restart(m.StreamParams.clamp())
	case "kf", "congested":
		a.wantKeyframe(m.T)
	case "wsvideo":
		a.mu.Lock()
		turnOn := m.On && !a.wsVideo
		a.wsVideo = m.On
		if turnOn {
			a.dropping = true // relay's GOP cache is stale: resume at a keyframe
		}
		a.mu.Unlock()
		log.Printf("relay video %s", map[bool]string{true: "on", false: "off (all viewers on P2P)"}[m.On])
		if turnOn {
			a.wantKeyframe("relay video on")
		}
	case "screen":
		a.mu.Lock()
		a.screen = m.I
		sp := a.params
		viewer := a.viewer
		a.mu.Unlock()
		log.Printf("viewer chose display %d", m.I)
		if viewer {
			a.restart(sp)
		}
	case "audio":
		a.mu.Lock()
		a.audioVids = map[int]bool{}
		for _, v := range m.Vids {
			a.audioVids[v] = true
		}
		a.wsAudio = m.WS
		a.mu.Unlock()
		a.syncAudio()
	case "ice":
		if len(m.Servers) > 0 {
			a.mu.Lock()
			a.ice = m.Servers
			a.mu.Unlock()
		}
	case "rtc":
		a.onRTC(m.Vid, m.SDP, m.Cand)
	case "rtc-bye":
		a.closePeer(m.Vid)
		a.closeFiles(m.Vid)
	case "fls", "fput", "fend", "fget", "fack", "fcancel", "fclip":
		a.onFile(&m)
	case "update":
		go func() {
			log.Printf("update requested by viewer")
			a.sendJSON(map[string]string{"t": "notice", "msg": "Đang tải bản cập nhật agent…"})
			if err := selfUpdate(a.cfg); err != nil {
				log.Printf("update: %v", err)
				a.sendJSON(map[string]string{"t": "notice", "msg": "Không cập nhật được: " + err.Error()})
				return
			}
			a.sendJSON(map[string]string{"t": "notice", "msg": "Đang cài bản mới — phiên sẽ tự nối lại sau khoảng 10–20 giây."})
		}()
	case "restart":
		log.Printf("restart requested by viewer")
		a.leave()
		restartSelf()
	}
}

func clamp01(v float64) float64 { return max(0, min(1, v)) }

// wantKeyframe: some viewer needs a fresh keyframe. ffmpeg can't be asked for
// one, so either wait for the natural one (every gopSeconds) if it is close,
// or restart the encoder.
func (a *Agent) wantKeyframe(why string) {
	a.mu.Lock()
	soon := time.Since(a.lastKey) > (gopSeconds-1)*time.Second
	recent := time.Since(a.lastKf) < 2*time.Second
	sp := a.params
	viewer := a.viewer
	a.mu.Unlock()
	if !viewer || soon || recent {
		return
	}
	log.Printf("keyframe needed (%s): restarting encoder", why)
	a.restart(sp)
}

func (a *Agent) join(sp StreamParams) {
	log.Printf("viewer joined (%d fps, ≤%dpx, %d kbps)", sp.FPS, sp.MaxW, sp.Kbps)
	a.mu.Lock()
	a.viewer = true
	a.wsVideo = true
	if a.awake == nil {
		a.awake = keepAwake()
	}
	if a.clip == nil {
		a.clip = startClipSync(
			func(text string) { a.sendJSON(map[string]string{"t": "clip", "text": text}) },
			func(paths []string) { a.sendJSON(map[string]any{"t": "clipfiles", "files": describeFiles(paths)}) })
	}
	a.mu.Unlock()
	a.input.Wake()
	a.restart(sp)
	a.syncAudio()
}

func (a *Agent) restart(sp StreamParams) {
	a.mu.Lock()
	old := a.sess
	a.sess = nil
	a.params = sp
	a.lastKf = time.Now()
	a.dropping = false
	viewer := a.viewer
	screen := a.screen
	a.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	if !viewer {
		return
	}
	displays := listDisplays()
	disp, idx := pickDisplay(displays, screen)
	sp.disp = disp
	a.inMu.Lock()
	a.input.SetDisplay(disp)
	a.inMu.Unlock()
	// Displays and audio go out now, not only once capture works: a display
	// that cannot be captured must not hide the way to pick another one.
	info := map[string]any{"t": "info", "os": runtime.GOOS, "screens": screenInfos(displays), "screen": idx, "audio": audioAvailable(a.ff)}
	a.sendJSON(info)
	s := startSession(a.ff, a.cfg, sp, a.sendFrame,
		func(p pipeline) {
			log.Printf("streaming display %d (%s, %dx%d) with %s", idx, disp.Name, disp.PxW, disp.PxH, p)
			info["enc"], info["capture"] = p.encoder, p.capture
			a.sendJSON(info)
		},
		func(err error) {
			log.Printf("capture failed: %v", err)
			a.sendJSON(map[string]string{"t": "error", "msg": "Không chụp được màn hình — đang tự thử lại…\n" + err.Error() + "\n" + captureHint()})
		})
	a.mu.Lock()
	if a.viewer && a.sess == nil {
		a.sess = s
		s = nil
	}
	a.mu.Unlock()
	if s != nil { // viewer left while we were starting
		s.Stop()
	}
}

func (a *Agent) leave() {
	a.mu.Lock()
	wasViewer := a.viewer
	a.viewer = false
	s := a.sess
	a.sess = nil
	stopAwake := a.awake
	a.awake = nil
	clip := a.clip
	a.clip = nil
	a.audioVids = nil
	a.mu.Unlock()
	a.syncAudio()
	if s != nil {
		s.Stop()
	}
	a.closeAllPeers()
	a.closeFiles(-1)
	if stopAwake != nil {
		stopAwake()
	}
	if clip != nil {
		clip.stop()
	}
	a.inMu.Lock()
	a.input.ReleaseAll()
	a.inMu.Unlock()
	if wasViewer {
		log.Printf("viewer left")
	}
}

// syncAudio starts or stops the audio capture to match audioVids.
func (a *Agent) syncAudio() {
	a.mu.Lock()
	want := a.viewer && len(a.audioVids) > 0
	running := a.audio
	if !want {
		a.audio = nil
	}
	a.mu.Unlock()
	if !want {
		if running != nil {
			running.Stop()
			log.Printf("audio off")
		}
		return
	}
	if running != nil {
		return
	}
	if !audioAvailable(a.ff) {
		a.sendJSON(map[string]string{"t": "notice", "msg": "Máy này không gửi được âm thanh (cần macOS 13+, hoặc ffmpeg có Opus)."})
		return
	}
	s := startAudio(a.ff, a.sendAudio, func(err error) {
		a.sendJSON(map[string]string{"t": "notice", "msg": "Không thu được âm thanh — đang thử lại…\n" + err.Error()})
	})
	a.mu.Lock()
	if a.audio == nil && a.viewer && len(a.audioVids) > 0 {
		a.audio, s = s, nil
	}
	a.mu.Unlock()
	if s != nil { // turned off (or started twice) meanwhile
		s.Stop()
		return
	}
	log.Printf("audio on")
}

// ---------------------------------------------------------------- clipboard

// clipSync polls the local clipboard while a viewer is attached and pushes
// changes to it; text from the viewer is written locally without echoing back.
// Copied files are announced too (the viewer offers to download them).
type clipSync struct {
	mu        sync.Mutex
	last      string
	lastFiles string
	quit      chan struct{}
}

func startClipSync(push func(string), pushFiles func([]string)) *clipSync {
	c := &clipSync{quit: make(chan struct{})}
	c.last, _ = clipboardRead()
	if fs, _ := clipboardReadFiles(); len(fs) > 0 {
		c.lastFiles = strings.Join(fs, "\n")
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.quit:
				return
			case <-t.C:
				if fs, err := clipboardReadFiles(); err == nil {
					key := strings.Join(fs, "\n")
					c.mu.Lock()
					changed := key != c.lastFiles
					c.lastFiles = key
					c.mu.Unlock()
					if changed && len(fs) > 0 {
						pushFiles(fs)
					}
				}
				text, err := clipboardRead()
				if err != nil || text == "" || len(text) > 1<<20 {
					continue
				}
				c.mu.Lock()
				changed := text != c.last
				c.last = text
				c.mu.Unlock()
				if changed {
					push(text)
				}
			}
		}
	}()
	return c
}

func (c *clipSync) setRemote(text string) {
	c.mu.Lock()
	c.last = text
	c.mu.Unlock()
	if err := clipboardWrite(text); err != nil {
		log.Printf("clipboard write: %v", err)
	}
}

// noteFiles: files the viewer is pasting here — not "copied on this machine".
func (c *clipSync) noteFiles(paths []string) {
	c.mu.Lock()
	c.lastFiles = strings.Join(paths, "\n")
	c.mu.Unlock()
}

func (c *clipSync) stop() { close(c.quit) }
