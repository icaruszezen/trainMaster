package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListen(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.2:0", ":0", "example.com:0", "nonsense"} {
		if ln, err := listen(addr); err == nil {
			ln.Close()
			t.Errorf("listen(%q) succeeded", addr)
		}
	}
	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	// The preferred port first; when it is taken, another one.
	if port != preferredPort {
		t.Logf("the preferred port %d is taken; got %d", preferredPort, port)
	}
	other, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Addr().(*net.TCPAddr).Port == port {
		t.Error("the same port twice")
	}
	if ln, err := listen("localhost:0"); err != nil {
		t.Errorf("localhost: %v", err)
	} else {
		ln.Close()
	}
}

func TestAppDir(t *testing.T) {
	if got := appDir(func() (string, error) { return "/home/u/.config", nil }); got != filepath.Join("/home/u/.config", appName) {
		t.Errorf("appDir = %q", got)
	}
	if got := appDir(func() (string, error) { return "", errors.New("no home") }); got != "" {
		t.Errorf("appDir without a folder = %q", got)
	}
}

func TestEmbeddedPlaceholder(t *testing.T) {
	data, err := fs.ReadFile(web, "web/placeholder.html")
	if err != nil || !strings.Contains(string(data), "make reviewer") {
		t.Errorf("placeholder: %v", err)
	}
}

func TestNewLogger(t *testing.T) {
	dir := t.TempDir()
	log, closeLog := newLogger(dir)
	log.Info("hello")
	closeLog()
	data, err := os.ReadFile(filepath.Join(dir, "reviewer.log"))
	if err != nil || !strings.Contains(string(data), "hello") {
		t.Errorf("log = %q, %v", data, err)
	}
}
