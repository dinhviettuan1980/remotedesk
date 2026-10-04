package main

// auSplitter turns ffmpeg's raw Annex B byte stream into access units (one
// encoded picture each), which is what the browser's VideoDecoder wants.
//
// A picture ends either when the next picture's first slice shows up
// (first_mb_in_slice == 0) or when the pipe goes quiet: ffmpeg writes each
// packet in one burst (-flush_packets 1), so a short idle gap means the frame
// is complete. The idle path is what keeps latency at ~0 extra frames.
type auSplitter struct {
	buf     []byte   // bytes not yet split into NAL units
	cur     [][]byte // NAL units of the picture being assembled
	curVCL  bool
	curKey  bool
	pending [][]byte // non-VCL NALs seen after cur's slices (belong to the next picture)
	emit    func(au []byte, key bool)
}

var startCode = []byte{0, 0, 0, 1}

// findStart returns the index of the next 00 00 01 start code at or after i,
// and the length of that start code (3 or 4), or -1.
func findStart(b []byte, i int) (int, int) {
	for ; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 {
			if b[i+2] == 1 {
				if i > 0 && b[i-1] == 0 {
					return i - 1, 4
				}
				return i, 3
			}
			if b[i+2] != 0 {
				i += 2
			}
		}
	}
	return -1, 0
}

func (s *auSplitter) feed(data []byte) {
	s.buf = append(s.buf, data...)
	for {
		start, sl := findStart(s.buf, 0)
		if start < 0 {
			// Keep the last couple of bytes: they may be the beginning of a start code.
			if len(s.buf) > 3 {
				s.buf = append(s.buf[:0], s.buf[len(s.buf)-3:]...)
			}
			return
		}
		next, _ := findStart(s.buf, start+sl)
		if next < 0 {
			if start > 0 {
				s.buf = append(s.buf[:0], s.buf[start:]...)
			}
			return
		}
		s.nal(s.buf[start+sl : next])
		s.buf = s.buf[next:]
	}
}

// idle is called when no bytes arrived for a few ms: the trailing NAL is
// complete, and so is the current picture.
func (s *auSplitter) idle() {
	if start, sl := findStart(s.buf, 0); start >= 0 && len(s.buf) > start+sl {
		s.nal(s.buf[start+sl:])
		s.buf = s.buf[:0]
	}
	if s.curVCL {
		s.flush()
	}
}

func (s *auSplitter) nal(p []byte) {
	if len(p) == 0 {
		return
	}
	n := append([]byte(nil), p...)
	typ := n[0] & 0x1f
	switch {
	case typ == 1 || typ == 5: // coded slice
		// first_mb_in_slice is ue(v); a leading 1 bit means it is 0 → new picture.
		firstSlice := len(n) > 1 && n[1]&0x80 != 0
		if firstSlice && s.curVCL {
			s.flush()
		}
		s.cur = append(s.cur, s.pending...)
		s.pending = s.pending[:0]
		s.cur = append(s.cur, n)
		s.curVCL = true
		if typ == 5 {
			s.curKey = true
		}
	case typ == 9 && s.curVCL: // access unit delimiter starts a new picture
		s.flush()
		s.cur = append(s.cur, n)
	case s.curVCL:
		s.pending = append(s.pending, n)
	default:
		s.cur = append(s.cur, n)
	}
}

func (s *auSplitter) flush() {
	size := 0
	for _, n := range s.cur {
		size += 4 + len(n)
	}
	au := make([]byte, 0, size)
	for _, n := range s.cur {
		au = append(au, startCode...)
		au = append(au, n...)
	}
	key := s.curKey
	s.cur, s.curVCL, s.curKey = s.cur[:0], false, false
	s.emit(au, key)
}
