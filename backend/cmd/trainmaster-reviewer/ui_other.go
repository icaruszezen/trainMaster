//go:build !windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
)

// Elsewhere than on Windows the program is for development: it prints the
// address (and opens it in the browser) and runs until Ctrl+C or until the
// page quits.

type platformUI struct{}

func (platformUI) closeWindow()       {}
func (platformUI) closeAcknowledged() {}

func webviewAvailable(*slog.Logger) bool { return false }

func (u *ui) run(served <-chan error) error {
	fmt.Printf("trainMaster 本地审图程序 %s\n请在浏览器中打开：%s\n按 Ctrl+C 退出。\n", version(), u.url)
	if !u.noOpen {
		if err := openBrowser(u.url); err != nil {
			u.log.Warn("open the browser", "err", err)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case <-sig:
	case <-u.quit:
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func (u *ui) fatal(msg string) { fmt.Fprintln(os.Stderr, msg) }

func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
