package main

import (
	"bytes"
	"errors"
	"io/fs"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/reviewer"
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

// TestServerAddress: the page's URL and the Host headers accepted are the
// address listened on, also for loopback addresses other than 127.0.0.1.
func TestServerAddress(t *testing.T) {
	for _, tc := range []struct {
		addr      string
		localhost bool
	}{{"127.0.0.1:0", true}, {"127.0.0.2:0", false}, {"[::1]:0", true}} {
		ln, err := listen(tc.addr)
		if err != nil {
			t.Logf("%s is not available here: %v", tc.addr, err)
			continue
		}
		hosts, base := serverAddress(ln.Addr().(*net.TCPAddr))
		port := ln.Addr().(*net.TCPAddr).Port
		if base != "http://"+ln.Addr().String() || hosts[0] != ln.Addr().String() ||
			slices.Contains(hosts, net.JoinHostPort("localhost", strconv.Itoa(port))) != tc.localhost {
			t.Errorf("%s: hosts %v, url %s (listening on %s)", tc.addr, hosts, base, ln.Addr())
		}
		srv := reviewer.New(reviewer.Options{Token: "t", Hosts: hosts})
		httpSrv := &http.Server{Handler: srv}
		go func() { _ = httpSrv.Serve(ln) }()
		client := &http.Client{Transport: &http.Transport{}} // no proxy
		resp, err := client.Get(base + "/api/session?token=t")
		if err != nil {
			t.Errorf("%s: %v", tc.addr, err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: GET %s/api/session: %d", tc.addr, base, resp.StatusCode)
			}
		}
		_ = httpSrv.Close()
		srv.Shutdown()
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestLibraryLog: the standard log package writes into the program's log,
// and log.Fatal (as go-webview2 calls it when WebView2 fails) is handed
// to onFatal before it would end the program.
func TestLibraryLog(t *testing.T) {
	var out syncBuffer
	l := &libraryLog{log: slog.New(slog.NewTextHandler(&out, nil))}
	flags, prefix, w := stdlog.Flags(), stdlog.Prefix(), stdlog.Writer()
	stdlog.SetFlags(0)
	stdlog.SetPrefix("")
	stdlog.SetOutput(l)
	t.Cleanup(func() {
		stdlog.SetFlags(flags)
		stdlog.SetPrefix(prefix)
		stdlog.SetOutput(w)
	})

	stdlog.Printf("Result: %08x", 0x80070005) // not fatal: no handler needed
	var fatal []string
	l.setOnFatal(func(msg string) {
		fatal = append(fatal, msg)
		runtime.Goexit() // does not return, as webviewFailed
	})
	for _, f := range []func(){
		func() { stdlog.Fatalf("Creating environment failed with %08x", 0x80070005) },
		func() { stdlog.New(l, "", 0).Fatal("Creating controller failed") },
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			f()
		}()
		<-done
	}
	stdlog.Print("after") // the log still works
	if strings.Join(fatal, "|") != "Creating environment failed with 80070005|Creating controller failed" {
		t.Errorf("fatal = %q", fatal)
	}
	got := out.String()
	for _, want := range []string{
		`level=WARN msg=library msg="Result: 80070005"`,
		`level=ERROR msg="library: fatal" msg="Creating environment failed with 80070005"`,
		`level=WARN msg=library msg=after`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %s:\n%s", want, got)
		}
	}
}
