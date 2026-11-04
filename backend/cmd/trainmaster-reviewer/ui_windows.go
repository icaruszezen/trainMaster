//go:build windows

package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// On Windows the page is shown in a window of the program's own, with the
// WebView2 runtime (part of Windows 10 and 11; Edge's engine). Without it
// the page opens in the default browser and a message box keeps the
// program running until the user clicks OK.
//
// Closing the window first asks the page (window.__tmReviewerRequestClose),
// which saves the last edit and then quits through POST /api/quit; it
// answers POST /api/closing at once to say it took over. A page that does
// not answer within closeAckTimeout (it did not load) is closed anyway.

func init() {
	// The window and WebView2 belong to the thread that creates them: keep
	// the main goroutine on the main thread.
	runtime.LockOSThread()
}

const windowTitle = "trainMaster 本地审图"

// closeAckTimeout is how long closing the window waits for the page.
const closeAckTimeout = 3 * time.Second

var (
	user32                            = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW               = user32.NewProc("CallWindowProcW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
)

const (
	wmClose           = 0x0010
	smCxScreen        = 0
	smCyScreen        = 1
	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	swShowNormal      = 1
)

var (
	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC (-4)
	// DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2): sharp on the main display;
	// per-monitor awareness would need the window resized on WM_DPICHANGED.
	dpiAwarenessSystemAware = ^uintptr(1)
)

// platformUI is the window while it is open.
type platformUI struct {
	mu       sync.Mutex
	w        webview2.WebView // nil when no window is open
	prevProc uintptr          // the window procedure of go-webview2
	force    atomic.Bool      // close without asking the page
	asked    atomic.Int64     // WM_CLOSE asks of the page so far
	acked    atomic.Int64     // the last ask the page took over
}

// webviewAvailable reports whether the WebView2 runtime is installed.
func webviewAvailable(log *slog.Logger) bool {
	v, err := webviewloader.GetInstalledVersion()
	switch {
	case err != nil:
		log.Warn("check the WebView2 runtime; using the browser", "err", err)
		return false
	case v == "":
		log.Info("the WebView2 runtime is not installed; using the browser")
		return false
	}
	log.Info("WebView2 runtime", "version", v)
	return true
}

func (u *ui) run(served <-chan error) error {
	if u.window {
		ok, err := u.runWindow(served)
		if ok || err != nil {
			return err
		}
		u.log.Warn("the WebView2 window could not be created; using the browser")
		u.window = false
		u.setWindow(false)
	}
	return u.runBrowser(served)
}

// runWindow shows the page in a window until it is closed; ok is false
// when the window could not be created.
func (u *ui) runWindow(served <-chan error) (ok bool, err error) {
	setDPIAware()
	scale := dpiScale()
	sw, sh := screenSize()
	width := min(int(1440*scale), sw*92/100)
	height := min(int(920*scale), sh*88/100)
	dataPath := ""
	if u.cacheDir != "" {
		dataPath = filepath.Join(u.cacheDir, "webview2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  dataPath,
		AutoFocus: true,
		Debug:     os.Getenv("TM_REVIEWER_DEBUG") != "", // developer tools and the context menu
		WindowOptions: webview2.WindowOptions{
			Title: windowTitle, Width: uint(max(width, 640)), Height: uint(max(height, 480)), Center: true,
		},
	})
	if w == nil {
		return false, nil
	}
	w.SetSize(int(800*scale), int(560*scale), webview2.HintMin)
	p := &u.platform
	p.attach(w, u.log)
	serveErr := make(chan error, 1)
	go func() {
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			p.closeWindow()
		}
	}()
	w.Navigate(u.url)
	w.Run()
	p.detach()
	select {
	case err := <-serveErr:
		return true, err
	default:
		return true, nil
	}
}

// attach makes the window ask the page before it closes.
func (p *platformUI) attach(w webview2.WebView, log *slog.Logger) {
	p.mu.Lock()
	p.w = w
	p.mu.Unlock()
	hwnd := uintptr(w.Window())
	prev, _, err := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, windows.NewCallback(p.wndProc))
	if prev == 0 {
		log.Warn("hook closing the window; it closes without saving the last edit first", "err", err)
		return
	}
	p.prevProc = prev
}

func (p *platformUI) detach() {
	p.mu.Lock()
	p.w = nil
	p.mu.Unlock()
}

func (p *platformUI) window() webview2.WebView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w
}

// wndProc sees the window's messages first (on the UI thread).
func (p *platformUI) wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmClose && !p.force.Load() {
		if w := p.window(); w != nil {
			ask := p.asked.Add(1)
			w.Eval("window.__tmReviewerRequestClose ? window.__tmReviewerRequestClose() : void 0")
			time.AfterFunc(closeAckTimeout, func() {
				if p.acked.Load() < ask {
					p.closeWindow()
				}
			})
			return 0
		}
	}
	r, _, _ := procCallWindowProcW.Call(p.prevProc, hwnd, msg, wparam, lparam)
	return r
}

// closeAcknowledged: the page took over the last close of the window.
func (p *platformUI) closeAcknowledged() { p.acked.Store(p.asked.Load()) }

// closeWindow closes the window without asking the page (any thread).
func (p *platformUI) closeWindow() {
	p.force.Store(true)
	if w := p.window(); w != nil {
		w.Destroy() // posts WM_CLOSE
	}
}

// runBrowser opens the page in the default browser and runs until the
// user confirms the message box or the page quits.
func (u *ui) runBrowser(served <-chan error) error {
	text := "已在浏览器中打开本地审图程序。点“确定”退出。"
	if u.noOpen {
		text = "本地审图程序正在运行，请在浏览器中打开：\n\n" + u.url + "\n\n点“确定”退出。"
	} else if err := openBrowser(u.url); err != nil {
		u.log.Warn("open the browser", "err", err)
		text = "无法打开浏览器，请在浏览器中打开：\n\n" + u.url + "\n\n点“确定”退出。"
	}
	confirmed := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		messageBox(text, mbOK|mbIconInformation|mbSetForeground)
		close(confirmed)
	}()
	select {
	case <-confirmed:
	case <-u.quit:
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func (u *ui) fatal(msg string) { messageBox(msg, mbOK|mbIconError|mbSetForeground) }

func messageBox(text string, flags uint32) {
	t, err1 := windows.UTF16PtrFromString(text)
	c, err2 := windows.UTF16PtrFromString(windowTitle)
	if err1 != nil || err2 != nil {
		return
	}
	_, _ = windows.MessageBox(0, t, c, flags)
}

// openBrowser opens url in the default browser.
func openBrowser(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, swShowNormal); err == nil {
		return nil
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func setDPIAware() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if r, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessSystemAware); r != 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

// dpiScale is the display scale (1 at 96 DPI, 1.5 at 150 %).
func dpiScale() float64 {
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			return float64(dpi) / 96
		}
	}
	return 1
}

func screenSize() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(smCxScreen)
	h, _, _ := procGetSystemMetrics.Call(smCyScreen)
	if w == 0 || h == 0 {
		return 1920, 1080
	}
	return int(w), int(h)
}
