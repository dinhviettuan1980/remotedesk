package main

import "testing"

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{
		{"0.3.2", "0.3.1", true},
		{"0.3.10", "0.3.9", true},
		{"0.4", "0.3.9", true},
		{"0.3.1", "0.3.1", false},
		{"0.3.0", "0.3.1", false},
		{"0.3.1.0", "0.3.1", false},
		{"", "0.3.1", false},
		{"<html>", "0.3.1", false},
		{"0.3.x", "0.3.1", false},
	} {
		if got := versionNewer(c.v, c.than); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.v, c.than, got, c.want)
		}
	}
}
