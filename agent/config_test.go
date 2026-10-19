package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/token-check" && r.Header.Get("Authorization") == "Bearer good-code" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "rdcfg.json")
	os.WriteFile(cfg, []byte(`{"server":"`+srv.URL+`/","app":true}`), 0o600)
	key := filepath.Join(dir, "key.txt")

	os.WriteFile(key, []byte("\xEF\xBB\xBF good-code \r\n"), 0o600) // BOM + spaces from a copy-paste
	if got := checkToken(cfg, key); got != 0 {
		t.Fatalf("good code: %d", got)
	}
	os.WriteFile(key, []byte("wrong"), 0o600)
	if got := checkToken(cfg, key); got != 3 {
		t.Fatalf("wrong code: %d", got)
	}
	if got := checkToken(cfg, ""); got != 3 {
		t.Fatalf("no code: %d", got)
	}
	os.WriteFile(cfg, []byte(`{"server":"http://127.0.0.1:1"}`), 0o600)
	os.WriteFile(key, []byte("good-code"), 0o600)
	if got := checkToken(cfg, key); got != 4 {
		t.Fatalf("unreachable: %d", got)
	}
}

func TestReadInstallerConfigKeepsLinkToken(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "rdcfg.json")
	os.WriteFile(cfg, []byte(`{"server":"https://x","token":"from-link"}`), 0o600)
	in, err := readInstallerConfig(cfg, filepath.Join(dir, "missing.txt"))
	if err != nil || in.Token != "from-link" {
		t.Fatalf("%v %q", err, in.Token)
	}
}
