package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// askUser: a dialog on this Mac, "no" when it times out or cannot be shown.
func askUser(title, msg string, timeout time.Duration) bool {
	q := strings.NewReplacer(`\`, `\`, `"`, `\"`).Replace
	script := fmt.Sprintf(`display dialog "%s" with title "%s" buttons {"Từ chối", "Cho phép"} default button "Từ chối" cancel button "Từ chối" with icon caution giving up after %d`,
		q(msg), q(title), int(timeout.Seconds()))
	out, err := exec.Command("osascript", "-e", script).Output() // "Từ chối" (cancel) exits non-zero
	return err == nil && strings.Contains(string(out), "button returned:Cho phép") && !strings.Contains(string(out), "gave up:true")
}
