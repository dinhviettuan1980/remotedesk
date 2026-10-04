package main

// File transfer over the relay WebSocket (works on any network, P2P or not).
// Every request carries the viewer's vid (stamped by the relay) and an id the
// viewer picked; replies go back with "to" = vid so only that viewer gets them.
//
//   viewer → agent  {"t":"fls","id":N,"path":"…"}                list a folder ("" = home)
//                   {"t":"fput","id":N,"name":"…","size":S}      upload into ~/Downloads
//                     (+ "clip":true,"batch":"…": into a temp folder, for paste)
//                   {"t":"fclip","id":N,"paths":[…]}             put uploaded files on the clipboard
//                   bin [2][vid u32][id u32][bytes]              upload data
//                   {"t":"fend","id":N}                          upload complete
//                   {"t":"fget","id":N,"path":"…"}               download a file
//                   {"t":"fack","id":N,"got":B}                  download flow control
//                   {"t":"fcancel","id":N}
//   agent → viewer  {"t":"fls","to":V,"id":N,"path","parent","roots","entries":[…]}
//                   {"t":"fmeta","to":V,"id":N,"name","size"}    download starts
//                   bin [2][vid u32][id u32][bytes]              download data
//                   {"t":"fack","to":V,"id":N,"got":B}           upload flow control
//                   {"t":"fdone","to":V,"id":N,"path":"…"}  /  {"t":"ferr","to":V,"id":N,"msg":"…"}
//                   {"t":"clipfiles","files":[…]}                files copied on this machine (all viewers)
//
// Both directions keep at most fileWindow bytes unacknowledged, so a big file
// never crowds out the video sharing the same socket.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	fileChunk  = 64 << 10
	fileWindow = 1 << 20
	fileAckGap = 256 << 10
)

type fileKey struct{ vid, id int }

type upload struct {
	f       *os.File
	path    string
	size    int64
	got     int64
	lastAck int64
}

type download struct {
	acked  chan int64 // latest "got" from the viewer
	cancel chan struct{}
}

type fileXfer struct {
	mu    sync.Mutex
	ups   map[fileKey]*upload
	downs map[fileKey]*download
}

func (a *Agent) fileReply(vid int, m map[string]any) {
	m["to"] = vid
	a.sendJSON(m)
}

func (a *Agent) fileErr(vid, id int, err error) {
	log.Printf("file[%d/%d]: %v", vid, id, err)
	a.fileReply(vid, map[string]any{"t": "ferr", "id": id, "msg": err.Error()})
}

// onFile handles the JSON side of file transfer.
func (a *Agent) onFile(m *inMsg) {
	x := &a.files
	k := fileKey{m.Vid, m.ID}
	switch m.T {
	case "fls":
		a.listDir(m.Vid, m.ID, m.Path)
	case "fput":
		x.mu.Lock()
		if old := x.ups[k]; old != nil {
			old.f.Close()
		}
		x.mu.Unlock()
		dir, err := uploadDir(m.Clip, m.Batch)
		if err != nil {
			a.fileErr(m.Vid, m.ID, err)
			return
		}
		up, err := createUpload(dir, m.Name, m.Size)
		if err != nil {
			a.fileErr(m.Vid, m.ID, err)
			return
		}
		x.mu.Lock()
		if x.ups == nil {
			x.ups = map[fileKey]*upload{}
		}
		x.ups[k] = up
		x.mu.Unlock()
		log.Printf("file[%d/%d]: receiving %s (%d bytes)", m.Vid, m.ID, up.path, m.Size)
	case "fend":
		x.mu.Lock()
		up := x.ups[k]
		delete(x.ups, k)
		x.mu.Unlock()
		if up == nil {
			return
		}
		err := up.f.Close()
		if err == nil && up.got != up.size {
			err = fmt.Errorf("thiếu dữ liệu (%d/%d byte)", up.got, up.size)
		}
		if err != nil {
			os.Remove(up.path)
			a.fileErr(m.Vid, m.ID, err)
			return
		}
		log.Printf("file[%d/%d]: saved %s", m.Vid, m.ID, up.path)
		a.fileReply(m.Vid, map[string]any{"t": "fdone", "id": m.ID, "path": up.path})
	case "fget":
		d := &download{acked: make(chan int64, 16), cancel: make(chan struct{})}
		x.mu.Lock()
		if x.downs == nil {
			x.downs = map[fileKey]*download{}
		}
		if old := x.downs[k]; old != nil {
			close(old.cancel)
		}
		x.downs[k] = d
		x.mu.Unlock()
		go a.sendFile(k, m.Path, d)
	case "fack":
		x.mu.Lock()
		d := x.downs[k]
		x.mu.Unlock()
		if d != nil {
			select {
			case d.acked <- m.Got:
			default:
			}
		}
	case "fclip":
		// Paste from the viewer: the files are uploaded; ⌘V/Ctrl+V follows.
		a.mu.Lock()
		clip := a.clip
		a.mu.Unlock()
		if clip != nil {
			clip.noteFiles(m.Paths)
		}
		if err := clipboardWriteFiles(m.Paths); err != nil {
			a.fileErr(m.Vid, m.ID, err)
			return
		}
		a.fileReply(m.Vid, map[string]any{"t": "fclip", "id": m.ID, "ok": true})
	case "fcancel":
		x.mu.Lock()
		if up := x.ups[k]; up != nil {
			up.f.Close()
			os.Remove(up.path)
			delete(x.ups, k)
		}
		if d := x.downs[k]; d != nil {
			close(d.cancel)
			delete(x.downs, k)
		}
		x.mu.Unlock()
	}
}

// onFileChunk takes one upload chunk: [2][vid u32][id u32][bytes].
func (a *Agent) onFileChunk(data []byte) {
	if len(data) < 9 {
		return
	}
	k := fileKey{int(binary.BigEndian.Uint32(data[1:5])), int(binary.BigEndian.Uint32(data[5:9]))}
	x := &a.files
	x.mu.Lock()
	up := x.ups[k]
	x.mu.Unlock()
	if up == nil {
		return
	}
	payload := data[9:]
	if up.got+int64(len(payload)) > up.size {
		a.onFile(&inMsg{T: "fcancel", Vid: k.vid, ID: k.id})
		a.fileErr(k.vid, k.id, errors.New("nhận nhiều dữ liệu hơn kích thước file"))
		return
	}
	if _, err := up.f.Write(payload); err != nil {
		a.onFile(&inMsg{T: "fcancel", Vid: k.vid, ID: k.id})
		a.fileErr(k.vid, k.id, err)
		return
	}
	up.got += int64(len(payload))
	if up.got-up.lastAck >= fileAckGap || up.got == up.size {
		up.lastAck = up.got
		a.fileReply(k.vid, map[string]any{"t": "fack", "id": k.id, "got": up.got})
	}
}

// uploadDir: ~/Downloads, or for a paste a fresh temp folder per batch (the
// paste copies the files out of it; batches older than a day are removed).
func uploadDir(clip bool, batch string) (string, error) {
	if !clip {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(home, "Downloads")
		return dir, os.MkdirAll(dir, 0o755)
	}
	base := filepath.Join(os.TempDir(), "RemoteDesk-paste")
	if ents, err := os.ReadDir(base); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				os.RemoveAll(filepath.Join(base, e.Name()))
			}
		}
	}
	b := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, batch)
	if b == "" {
		b = fmt.Sprint(time.Now().UnixNano())
	}
	dir := filepath.Join(base, b)
	return dir, os.MkdirAll(dir, 0o755)
}

// createUpload opens a new file in dir, never overwriting: "a.txt" becomes
// "a (1).txt" when taken.
func createUpload(dir, name string, size int64) (*upload, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == "/" {
		return nil, errors.New("tên file không hợp lệ")
	}
	if size < 0 {
		return nil, errors.New("kích thước không hợp lệ")
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, name)
		if i > 0 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &upload{f: f, path: p, size: size}, nil
	}
	return nil, errors.New("quá nhiều file trùng tên")
}

func (a *Agent) sendFile(k fileKey, path string, d *download) {
	defer func() {
		a.files.mu.Lock()
		if a.files.downs[k] == d {
			delete(a.files.downs, k)
		}
		a.files.mu.Unlock()
	}()
	f, err := os.Open(path)
	if err != nil {
		a.fileErr(k.vid, k.id, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		a.fileErr(k.vid, k.id, errors.New("không phải file"))
		return
	}
	log.Printf("file[%d/%d]: sending %s (%d bytes)", k.vid, k.id, path, st.Size())
	a.fileReply(k.vid, map[string]any{"t": "fmeta", "id": k.id, "name": filepath.Base(path), "size": st.Size()})
	var sent, acked int64
	buf := make([]byte, fileChunk)
	for sent < st.Size() {
		for sent-acked >= fileWindow { // wait for the viewer to catch up
			select {
			case g := <-d.acked:
				acked = max(acked, g)
			case <-d.cancel:
				return
			case <-time.After(60 * time.Second):
				a.fileErr(k.vid, k.id, errors.New("người xem không phản hồi"))
				return
			}
		}
		n, err := f.Read(buf)
		if n > 0 {
			msg := make([]byte, 9+n)
			msg[0] = 2
			binary.BigEndian.PutUint32(msg[1:5], uint32(k.vid))
			binary.BigEndian.PutUint32(msg[5:9], uint32(k.id))
			copy(msg[9:], buf[:n])
			if !a.sendBinary(msg, d.cancel) {
				return
			}
			sent += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			a.fileErr(k.vid, k.id, err)
			return
		}
	}
	a.fileReply(k.vid, map[string]any{"t": "fdone", "id": k.id, "path": path})
}

// sendBinary queues a non-video binary message, waiting while the relay
// queue is busy (video keeps priority: it never waits for files).
func (a *Agent) sendBinary(msg []byte, cancel chan struct{}) bool {
	for {
		a.mu.Lock()
		if a.out == nil {
			a.mu.Unlock()
			return false
		}
		if len(a.out) < cap(a.out)/2 {
			a.out <- outMsg{websocket.BinaryMessage, msg}
			a.mu.Unlock()
			return true
		}
		a.mu.Unlock()
		select {
		case <-cancel:
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type fileEntry struct {
	N string `json:"n"`           // name
	D bool   `json:"d,omitempty"` // directory
	S int64  `json:"s,omitempty"` // size
	M int64  `json:"m,omitempty"` // modified, unix ms
}

func (a *Agent) listDir(vid, id int, path string) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			a.fileErr(vid, id, err)
			return
		}
		path = home
	}
	path = filepath.Clean(path)
	ents, err := os.ReadDir(path)
	if err != nil {
		a.fileErr(vid, id, err)
		return
	}
	out := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		if len(out) >= 5000 {
			break
		}
		fe := fileEntry{N: e.Name(), D: e.IsDir()}
		if info, err := e.Info(); err == nil {
			if !fe.D {
				fe.S = info.Size()
			}
			fe.M = info.ModTime().UnixMilli()
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].D != out[j].D {
			return out[i].D
		}
		return strings.ToLower(out[i].N) < strings.ToLower(out[j].N)
	})
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	a.fileReply(vid, map[string]any{"t": "fls", "id": id, "path": path, "parent": parent, "roots": fileRoots(), "entries": out})
}

// fileRoots: shortcuts shown in the viewer's file panel.
func fileRoots() []string {
	home, _ := os.UserHomeDir()
	roots := []string{home, filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"), filepath.Join(home, "Documents")}
	if runtime.GOOS == "windows" {
		for c := 'C'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				roots = append(roots, d)
			}
		}
	} else {
		roots = append(roots, "/")
	}
	return roots
}

// closeFiles drops the transfers of viewer vid (-1 = all: last viewer gone
// or relay connection lost).
func (a *Agent) closeFiles(vid int) {
	x := &a.files
	x.mu.Lock()
	defer x.mu.Unlock()
	for k, up := range x.ups {
		if vid < 0 || k.vid == vid {
			up.f.Close()
			os.Remove(up.path)
			delete(x.ups, k)
		}
	}
	for k, d := range x.downs {
		if vid < 0 || k.vid == vid {
			close(d.cancel)
			delete(x.downs, k)
		}
	}
}

type clipFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir,omitempty"`
}

// describeFiles turns copied paths into what the viewer shows.
func describeFiles(paths []string) []clipFile {
	out := make([]clipFile, 0, len(paths))
	for _, p := range paths {
		cf := clipFile{Path: p, Name: filepath.Base(p)}
		if st, err := os.Stat(p); err == nil {
			cf.Dir = st.IsDir()
			if !cf.Dir {
				cf.Size = st.Size()
			}
		}
		out = append(out, cf)
	}
	return out
}
