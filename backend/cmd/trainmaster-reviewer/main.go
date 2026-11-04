// Command trainmaster-reviewer is the local reviewer (本地审图程序): a
// Windows program that opens a review package downloaded from trainMaster,
// shows the platform's review editor on it in a window of its own
// (WebView2), and exports the results to upload back. Built for other
// systems it serves the same page for a browser (for development).
//
//	trainmaster-reviewer [-addr 127.0.0.1:0] [-browser] [-no-open] [审图包.zip | 文件夹]
//
// A package given on the command line (or dropped on the program's icon)
// is opened at start. See internal/reviewer for the server.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewer"
)

// web holds the frontend build (web/dist, made by npm run build:reviewer,
// not committed) and the committed placeholder page served without it.
//
//go:embed all:web
var web embed.FS

// appName names the program's folders in the user's configuration and
// cache folders.
const appName = "trainmaster-reviewer"

// preferredPort is tried first when no port is given: the page then has
// the same address every time, so the browser (WebView2) keeps its
// settings (theme, editor preferences) from one run to the next.
const preferredPort = 47821

// maxLogBytes is the size at which the log file is started anew (the old
// one is kept as reviewer.log.1).
const maxLogBytes = 4 << 20

func main() { os.Exit(run()) }

func run() int {
	flags := flag.NewFlagSet(appName, flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:0", "监听地址（只能是本机地址；端口为 0 时自动选择）")
	browser := flags.Bool("browser", false, "在默认浏览器中打开，不使用程序窗口")
	noOpen := flags.Bool("no-open", false, "不自动打开窗口或浏览器，只显示访问地址")
	showVersion := flags.Bool("version", false, "显示版本后退出")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "trainMaster 本地审图程序 %s\n\n用法：%s [选项] [审图包.zip | 审图包文件夹]\n\n", version(), appName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Println("trainMaster 本地审图程序", version())
		return 0
	}
	open := flags.Arg(0)
	if open != "" {
		if abs, err := filepath.Abs(open); err == nil {
			open = abs
		}
	}

	configDir, cacheDir := appDir(os.UserConfigDir), appDir(os.UserCacheDir)
	log, closeLog := newLogger(cacheDir)
	defer closeLog()

	u := newUI(*browser, *noOpen, cacheDir, log)
	ln, err := listen(*addr)
	if err != nil {
		u.fatal(fmt.Sprintf("本地审图程序无法启动：%v", err))
		return 1
	}
	port := ln.Addr().(*net.TCPAddr).Port
	token := rand.Text()
	webFS, err := fs.Sub(web, "web")
	if err != nil {
		panic(err) // the embedded folder is always there
	}
	if dir := os.Getenv("TM_REVIEWER_WEB_DIR"); dir != "" {
		// Development: the page from disk (a folder holding dist/, such as
		// cmd/trainmaster-reviewer/web), rebuilt without rebuilding the program.
		webFS = os.DirFS(dir)
		log.Info("serving the page from disk", "dir", dir)
	}
	srv := reviewer.New(reviewer.Options{
		Web:        webFS,
		Token:      token,
		Hosts:      []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), net.JoinHostPort("localhost", strconv.Itoa(port))},
		CookieName: "tm_reviewer_" + strconv.Itoa(port),
		Version:    version(),
		ConfigDir:  configDir,
		CacheDir:   cacheDir,
		Logger:     log,
		Window:     u.window,
		Quit:       u.requestQuit,
		Closing:    u.closing,
	})
	u.setWindow = srv.SetWindow
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
	served := make(chan error, 1)
	go func() { served <- httpSrv.Serve(ln) }()
	log.Info("local reviewer started", "version", version(), "addr", ln.Addr().String(), "window", u.window,
		"config", configDir, "cache", cacheDir)

	if open != "" {
		if _, err := srv.Open(open); err != nil {
			srv.NoteOpenError(open, err)
			log.Warn("open package given on the command line", "path", open, "err", err)
		}
	}

	// With a package to open the page starts in the editor (it shows the
	// start page with the progress until the package is open, or why it
	// cannot be opened).
	page := "/"
	if open != "" {
		page = "/review"
	}
	u.url = fmt.Sprintf("http://127.0.0.1:%d%s?token=%s", port, page, token)
	uiErr := u.run(served)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	srv.Shutdown()
	if uiErr != nil {
		log.Error("local reviewer stopped", "err", uiErr)
		u.fatal(fmt.Sprintf("本地审图程序出错：%v", uiErr))
		return 1
	}
	log.Info("local reviewer stopped")
	return 0
}

// version is the program's version: that of the platform it is built with.
func version() string {
	if buildinfo.Commit != "" && buildinfo.Commit != "unknown" {
		return buildinfo.Version + " (" + buildinfo.Commit + ")"
	}
	return buildinfo.Version
}

// listen listens on addr, which must be a loopback address. Port 0 tries
// preferredPort first.
func listen(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q 无效：%w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("监听地址 %q 不是本机地址：本地审图程序只能在本机（127.0.0.1）上访问", addr)
	}
	if port == "0" {
		if ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(preferredPort))); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, port))
}

// appDir returns the program's folder inside a user folder ("" when there
// is none).
func appDir(base func() (string, error)) string {
	dir, err := base()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, appName)
}

// newLogger logs to reviewer.log in the cache folder (the Windows program
// has no console) and to stderr.
func newLogger(cacheDir string) (*slog.Logger, func()) {
	var w io.Writer = os.Stderr
	closeLog := func() {}
	if cacheDir != "" && os.MkdirAll(cacheDir, 0o755) == nil {
		p := filepath.Join(cacheDir, "reviewer.log")
		if fi, err := os.Stat(p); err == nil && fi.Size() > maxLogBytes {
			_ = os.Rename(p, p+".1")
		}
		if f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err == nil {
			// The file first: on Windows stderr may not exist, and
			// MultiWriter stops at the first writer that fails.
			w = io.MultiWriter(f, os.Stderr)
			closeLog = func() { _ = f.Close() }
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})), closeLog
}

// ui is the program's window (or the browser it opened) around the
// server's page. Its platform parts are in ui_windows.go and ui_other.go.
type ui struct {
	url       string
	browser   bool // the user asked for a browser
	noOpen    bool
	window    bool // the page is shown in the program's own window
	cacheDir  string
	log       *slog.Logger
	setWindow func(bool) // tells the server when the window falls back to a browser

	quitOnce sync.Once
	quit     chan struct{} // closed when the page asks to quit
	platform platformUI
}

func newUI(browser, noOpen bool, cacheDir string, log *slog.Logger) *ui {
	u := &ui{browser: browser, noOpen: noOpen, cacheDir: cacheDir, log: log, quit: make(chan struct{})}
	u.window = !browser && !noOpen && webviewAvailable(log)
	return u
}

// requestQuit is POST /api/quit: close the window, or stop when the page
// is in a browser.
func (u *ui) requestQuit() {
	u.quitOnce.Do(func() { close(u.quit) })
	u.platform.closeWindow()
}

// closing is POST /api/closing: the page took over closing the window.
func (u *ui) closing() { u.platform.closeAcknowledged() }
