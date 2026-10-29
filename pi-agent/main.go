// RemoteDesk agent for Raspberry Pi (and other headless Linux boxes).
//
// Keeps one outbound WebSocket to the relay, like the desktop agent, so a Pi
// behind NAT or 4G is reachable from the web viewer: system overview,
// systemd services, attached hardware, a terminal, reboot and self-update.
// Runs as root under systemd (install/install-pi.sh).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

const version = "0.1.0"

const defaultConfig = "/etc/remotedesk-pi/config.json"

type Config struct {
	Server string `json:"server"` // e.g. https://remote.tuandv.id.vn
	Token  string `json:"token"`  // access code of the group this Pi joins
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	// ShellUser: who the web terminal logs in as (default: the uid 1000 user, else root).
	ShellUser string `json:"shellUser,omitempty"`
	// Watch: services listed first on the Dịch vụ tab (glob patterns).
	Watch []string `json:"watch,omitempty"`
}

var defaultWatch = []string{"linh-dia-viet*", "ld-*", "remotedesk-pi", "ssh", "NetworkManager", "bluetooth", "bluealsa*", "mosquitto"}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server == "" || c.Token == "" {
		return nil, fmt.Errorf("%s: server and token are required", path)
	}
	c.Server = strings.TrimRight(c.Server, "/")
	host, _ := os.Hostname()
	if c.Name == "" {
		c.Name = host
	}
	if c.ID == "" {
		// Hostnames repeat (every fresh image is "raspberrypi"): add the machine id.
		c.ID = slug(host)
		if mid, err := os.ReadFile("/etc/machine-id"); err == nil && len(strings.TrimSpace(string(mid))) >= 6 {
			c.ID += "-" + strings.TrimSpace(string(mid))[:6]
		}
	}
	if len(c.Watch) == 0 {
		c.Watch = defaultWatch
	}
	return &c, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	if s = strings.Trim(s, "-"); s == "" {
		return "pi"
	}
	return s
}

func main() {
	cfgPath := flag.String("config", defaultConfig, "path to config.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("pi-agent: ")
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s starting as %q → %s", version, cfg.ID, cfg.Server)
	a := newAgent(cfg)
	backoff := time.Second
	for {
		start := time.Now()
		err := a.runOnce()
		if errors.Is(err, errReplaced) {
			// Another machine connected with this id: back off instead of
			// taking it over again right away.
			log.Printf("replaced by another connection with id %q; retrying in 1 min", cfg.ID)
			time.Sleep(time.Minute)
			continue
		}
		log.Printf("disconnected: %v", err)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}
