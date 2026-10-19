package main

import "os/exec"

// runInUserSession starts a helper program in the signed-in user's session.
// The agent already runs there (logon task), so this is a plain start.
func runInUserSession(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	return cmd.Start()
}
