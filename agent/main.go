// RemoteDesk agent: runs on the machine being controlled. It keeps an outbound
// WebSocket to the relay, and while a viewer is attached it streams the screen
// (ffmpeg → H.264) and injects the viewer's mouse/keyboard input.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const version = "0.2.1"

func main() {
	cfgPath := flag.String("config", "", "path to config.json (default: next to the binary, then user config dir)")
	check := flag.Bool("check", false, "self-test: detect encoders, capture 3 s of screen, then exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	importCfg := flag.String("import-config", "", "installer use: merge server+token from this JSON file into config.json and exit")
	flag.Parse()

	if *importCfg != "" {
		if err := importConfig(*importCfg); err != nil {
			attachConsole()
			fmt.Fprintln(os.Stderr, "import-config:", err)
			os.Exit(1)
		}
		return
	}

	if *check || *showVersion {
		attachConsole()
	}
	if *showVersion {
		fmt.Println("remote-agent", version)
		return
	}

	cfg, path, err := loadConfig(*cfgPath)
	if err != nil {
		attachConsole()
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	setupLog(filepath.Dir(path), *check)
	log.Printf("remote-agent %s starting (config %s, id %q)", version, path, cfg.ID)

	platformInit()

	ff, err := findFFmpeg(cfg, filepath.Dir(path))
	if err != nil {
		log.Fatalf("ffmpeg: %v", err)
	}
	log.Printf("ffmpeg: %s", ff)

	if *check {
		os.Exit(selfTest(cfg, ff))
	}

	a := newAgent(cfg, ff)
	backoff := time.Second
	for {
		start := time.Now()
		err := a.runOnce()
		if err == errReplaced {
			// Another agent with the same id took over; don't fight it.
			log.Printf("replaced by another agent with id %q; retrying in 60s", cfg.ID)
			time.Sleep(60 * time.Second)
			continue
		}
		if err != nil && strings.Contains(err.Error(), "HTTP 401") {
			// Token revoked or rotated on the relay: nothing will change soon.
			log.Printf("token rejected by the relay (revoked?); reinstall with a new token. Retrying in 5m")
			time.Sleep(5 * time.Minute)
			continue
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("disconnected: %v (retry in %s)", err, backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// setupLog writes to agent.log next to the config (truncated when > 5 MB),
// and to stderr when running interactively.
func setupLog(dir string, interactive bool) {
	p := filepath.Join(dir, "agent.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if interactive {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	} else {
		log.SetOutput(f)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
