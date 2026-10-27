package main

import (
	"log"
	"time"
)

// Consent: with "confirm": true in config.json a session starts only after
// someone at this machine clicks "Cho phép" (30 s, then it counts as a refusal).
// Until then nothing is captured, no input is applied and no clipboard, file
// or audio data leaves; a refusal tells the viewers to go ({"t":"deny"}).
// Deliberately not settable from a viewer ("setcfg"): whoever it guards
// against could turn it off. Viewers joining a session already allowed are
// only announced (notification), not asked again.

const consentTimeout = 30 * time.Second

// gated: the session is waiting for (or was refused) consent. Caller holds a.mu.
func (a *Agent) gated() bool {
	return a.cfg.Confirm && !a.approved
}

func (a *Agent) isGated() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gated()
}

func (a *Agent) askConsent() {
	a.mu.Lock()
	if !a.gated() || a.asking {
		a.mu.Unlock()
		return
	}
	a.asking = true
	from := a.askFrom
	a.mu.Unlock()
	if from == "" {
		from = "Một người"
	}
	log.Printf("consent: asking (%s)", from)
	a.sendJSON(map[string]string{"t": "notice", "msg": "Đang chờ người ở máy kia cho phép…"})
	ok := askUser("RemoteDesk", from+" muốn xem và điều khiển máy này.\n\nCho phép?", consentTimeout)

	a.mu.Lock()
	a.asking = false
	a.approved = ok
	viewer := a.viewer
	sp := a.params
	a.mu.Unlock()
	log.Printf("consent: %v", ok)
	if !ok {
		a.sendJSON(map[string]string{"t": "deny", "msg": "Người ở máy kia không cho phép (hoặc không trả lời)."})
		return
	}
	if viewer {
		a.startViewing(sp)
	}
}
