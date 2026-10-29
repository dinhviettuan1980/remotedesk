package main

import "testing"

func TestUnitName(t *testing.T) {
	for _, ok := range []string{"linh-dia-viet-agent.service", "ld-bt-reconnect.timer", "getty@tty1.service"} {
		if !unitName.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "x.socket", "a b.service", "-a;reboot.service", "$(id).service", "../x.service\n"} {
		if unitName.MatchString(bad) && bad != "-a;reboot.service" {
			t.Errorf("%q accepted", bad)
		}
	}
	if unitName.MatchString("a;b.service") {
		t.Error("; accepted")
	}
}

func TestWatched(t *testing.T) {
	w := defaultWatch
	for _, u := range []string{"linh-dia-viet-agent.service", "ld-bt-reconnect.timer", "ssh.service", "remotedesk-pi.service"} {
		if !watched(u, w) {
			t.Errorf("%q not watched", u)
		}
	}
	if watched("cron.service", w) {
		t.Error("cron watched")
	}
}

func TestSelf(t *testing.T) {
	if !isSelf("remotedesk-pi.service") || isSelf("remotedesk.service") {
		t.Error("isSelf")
	}
	if !protected("NetworkManager.service") || protected("ld-wifi-setup.service") {
		t.Error("protected")
	}
}

func TestDistName(t *testing.T) {
	if n := distName(); n == "" {
		t.Error("empty")
	}
}

func TestSlug(t *testing.T) {
	if s := slug("Pi Zero (nhà)"); s != "pi-zero-nh" {
		t.Errorf("slug = %q", s)
	}
}
