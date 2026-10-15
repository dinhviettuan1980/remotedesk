package main

// Direct (P2P) path: per viewer, a WebRTC peer connection carries the video as
// an RTP track (NACK retransmission, PLI) and the input over a data channel,
// skipping the relay. Signaling rides on the relay WebSocket:
//
//   viewer → relay → agent  {"t":"rtc","vid":N,"sdp":{offer}} / {"t":"rtc","vid":N,"cand":{…}}
//   agent → relay → viewer  {"t":"rtc","to":N,"sdp":{answer}} / {"t":"rtc","to":N,"cand":{…}}
//   relay → agent           {"t":"rtc-bye","vid":N}   (viewer left)
//
// The relay path stays the fallback: video starts there and the viewer
// switches over once the P2P track shows a picture.

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

var errNotStreaming = errors.New("not streaming")

// Used until the relay sends {"t":"ice"} (relays from before TURN never do).
var iceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"}}}

var (
	rtcAPIOnce sync.Once
	rtcAPI     *webrtc.API
	rtcAPIErr  error
)

// newRTCAPI registers only H.264 (what ffmpeg produces). The encoder emits
// High profile; Constrained Baseline is listed too so a browser that only
// offers that still connects (decoders accept the higher profile in practice).
func newRTCAPI() (*webrtc.API, error) {
	rtcAPIOnce.Do(func() {
		m := &webrtc.MediaEngine{}
		fb := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "transport-cc"}}
		for i, plid := range []string{"64001f", "640c1f", "42e01f", "42001f"} {
			err := m.RegisterCodec(webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
					SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=" + plid,
					RTCPFeedback: fb,
				},
				PayloadType: webrtc.PayloadType(102 + 2*i),
			}, webrtc.RTPCodecTypeVideo)
			if err != nil {
				rtcAPIErr = err
				return
			}
		}
		// NACK generator/responder, RTCP reports, TWCC header extension.
		reg := &interceptor.Registry{}
		if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
			rtcAPIErr = err
			return
		}
		rtcAPI = webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg))
	})
	return rtcAPI, rtcAPIErr
}

type peer struct {
	vid    int
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticSample
	frames chan p2pFrame

	// guarded by Agent.mu
	connected bool
	waitKey   bool // send nothing until the next keyframe (start / after a drop)
	closed    bool
	since     time.Time
}

type p2pFrame struct {
	au []byte
	at time.Time
}

type rtcOut struct {
	T    string                     `json:"t"`
	To   int                        `json:"to"`
	SDP  *webrtc.SessionDescription `json:"sdp,omitempty"`
	Cand *webrtc.ICECandidateInit   `json:"cand,omitempty"`
	Err  string                     `json:"err,omitempty"`
}

// onRTC handles one signaling message from viewer vid.
func (a *Agent) onRTC(vid int, sdp *webrtc.SessionDescription, cand json.RawMessage) {
	if sdp != nil && sdp.Type == webrtc.SDPTypeOffer {
		if err := a.answerPeer(vid, *sdp); err != nil {
			log.Printf("p2p[%d]: %v", vid, err)
			a.sendJSON(rtcOut{T: "rtc", To: vid, Err: err.Error()})
		}
		return
	}
	if len(cand) == 0 || string(cand) == "null" {
		return
	}
	var c webrtc.ICECandidateInit
	if json.Unmarshal(cand, &c) != nil || c.Candidate == "" {
		return
	}
	a.mu.Lock()
	p := a.peers[vid]
	a.mu.Unlock()
	if p != nil {
		if err := p.pc.AddICECandidate(c); err != nil {
			log.Printf("p2p[%d]: add candidate: %v", vid, err)
		}
	}
}

func (a *Agent) answerPeer(vid int, offer webrtc.SessionDescription) error {
	api, err := newRTCAPI()
	if err != nil {
		return err
	}
	a.mu.Lock()
	viewer := a.viewer
	ice := a.ice
	a.mu.Unlock()
	if !viewer {
		return errNotStreaming
	}
	if len(ice) == 0 {
		ice = iceServers
	}
	a.closePeer(vid) // the viewer renegotiates from scratch after a failure

	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice, BundlePolicy: webrtc.BundlePolicyMaxBundle})
	if err != nil {
		return err
	}
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "remotedesk")
	if err != nil {
		pc.Close()
		return err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return err
	}
	p := &peer{vid: vid, pc: pc, track: track, frames: make(chan p2pFrame, 30), waitKey: true, since: time.Now()}

	go a.readRTCP(p, sender)
	go p.writeLoop()

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		ci := c.ToJSON()
		a.sendJSON(rtcOut{T: "rtc", To: vid, Cand: &ci})
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("p2p[%d]: %s", vid, s)
		switch s {
		case webrtc.PeerConnectionStateConnected:
			a.mu.Lock()
			if !p.closed {
				p.connected = true
				p.waitKey = true // the next natural keyframe starts the track (≤ one GOP)
			}
			a.mu.Unlock()
			if pair, err := sender.Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
				log.Printf("p2p[%d]: via %s ⇄ %s", vid, pair.Local, pair.Remote)
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			a.closePeerIf(p)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if msg.IsString {
				a.handleInput(msg.Data)
			}
		})
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return err
	}
	a.mu.Lock()
	if a.peers == nil {
		a.peers = map[int]*peer{}
	}
	a.peers[vid] = p
	a.mu.Unlock()
	// Candidates gathered so far may already be queued ahead of the answer;
	// the viewer buffers them until it has the answer.
	a.sendJSON(rtcOut{T: "rtc", To: vid, SDP: pc.LocalDescription()})
	log.Printf("p2p[%d]: answered", vid)
	return nil
}

// readRTCP drains RTCP (required for the NACK responder) and turns picture
// loss reports into keyframe requests.
func (a *Agent) readRTCP(p *peer, sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, pkt := range pkts {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				a.mu.Lock()
				warm := time.Since(p.since) < 5*time.Second // browsers send PLI while the track starts up
				a.mu.Unlock()
				if !warm {
					a.wantKeyframe("p2p PLI")
				}
			}
		}
	}
}

func (p *peer) writeLoop() {
	var last time.Time
	for f := range p.frames {
		d := f.at.Sub(last)
		if last.IsZero() || d <= 0 || d > time.Second {
			d = time.Second / 30
		}
		last = f.at
		if err := p.track.WriteSample(media.Sample{Data: f.au, Duration: d}); err != nil {
			log.Printf("p2p[%d]: write: %v", p.vid, err)
		}
	}
}

// sendP2P fans one access unit out to the connected peers. Called with a.mu held.
func (a *Agent) sendP2P(au []byte, key bool, at time.Time) {
	for _, p := range a.peers {
		if !p.connected || p.closed {
			continue
		}
		if p.waitKey {
			if !key {
				continue
			}
			p.waitKey = false
		}
		select {
		case p.frames <- p2pFrame{au, at}:
		default:
			// The peer's writer is stuck (should be rare: UDP writes don't
			// block). Resync on the next keyframe rather than send a broken chain.
			p.waitKey = true
			log.Printf("p2p[%d]: queue full, waiting for keyframe", p.vid)
		}
	}
}

func (a *Agent) closePeer(vid int) {
	a.mu.Lock()
	p := a.peers[vid]
	a.mu.Unlock()
	if p != nil {
		a.closePeerIf(p)
	}
}

// closePeerIf removes p if it is still the current peer for its viewer.
func (a *Agent) closePeerIf(p *peer) {
	a.mu.Lock()
	if p.closed {
		a.mu.Unlock()
		return
	}
	p.closed = true
	if a.peers[p.vid] == p {
		delete(a.peers, p.vid)
	}
	close(p.frames)
	a.mu.Unlock()
	go p.pc.Close() // may block briefly on DTLS close; never hold up the read loop
}

func (a *Agent) closeAllPeers() {
	a.mu.Lock()
	ps := make([]*peer, 0, len(a.peers))
	for _, p := range a.peers {
		ps = append(ps, p)
	}
	a.mu.Unlock()
	for _, p := range ps {
		a.closePeerIf(p)
	}
}
