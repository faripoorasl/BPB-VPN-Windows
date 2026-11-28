//go:build windows

// The browser dialer (BrowserDialer.kt on Windows): Xray serves its dialer page and opens
// every WebSocket through it; a headless Chromium (Edge, on every Windows 10/11) loads that
// page, so the TLS is Chromium's. The service is outside the TUN, so it connects directly.
//
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	xray "bpb/core/xray"
)

const browserDialerAddress = "127.0.0.1:10832"

var (
	dialerMu       sync.Mutex
	dialerCommands []*exec.Cmd
)

// startBrowserDialer turns the dialer on and loads the page in a hidden headless browser.
func startBrowserDialer(address string) {
	stopBrowserDialer()
	xray.SetBrowserDialer(address)
	for _, browser := range browserCandidates() {
		cmd := exec.Command(browser,
			"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
			"--disable-features=Translate", "--mute-audio", "--window-size=400,300",
			"--user-data-dir="+browserDialerProfile(),
			"http://"+address+"/")
		if err := cmd.Start(); err == nil {
			dialerMu.Lock()
			dialerCommands = append(dialerCommands, cmd)
			dialerMu.Unlock()
			return
		}
	}
	// The dialer stays on: Xray opens its WebSockets itself when no page is connected.
	logf("browser dialer: no Chromium-based browser found")
}

// stopBrowserDialer turns the dialer off and drops the browser.
func stopBrowserDialer() {
	xray.SetBrowserDialer("")
	dialerMu.Lock()
	defer dialerMu.Unlock()
	for _, cmd := range dialerCommands {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	dialerCommands = nil
}

// browserCandidates: Edge first (bundled with Windows), then Chrome.
func browserCandidates() []string {
	paths := []string{}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		paths = append(paths,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	return paths
}

// browserDialerProfile: its own profile dir, off the record.
func browserDialerProfile() string {
	if dir, err := dataDir(); err == nil {
		return filepath.Join(dir, "browser-dialer")
	}
	return ""
}
