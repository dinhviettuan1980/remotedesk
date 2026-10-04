package main

import "errors"

// selfUpdate: not on macOS yet — a new (ad-hoc signed) binary would lose the
// Screen Recording / Accessibility grants, which can't be re-granted remotely.
func selfUpdate(*Config) error {
	return errors.New("chưa hỗ trợ trên macOS — cài lại bằng bộ cài .pkg tại máy")
}
