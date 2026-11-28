//go:build !windows

// Non-Windows stubs, so the host builds (and the integration tests run) on other platforms.
//
package main

import "os/exec"

func isElevated() bool { return true }

func wintunFound(binDir string) bool { return true }

func openURL(url string) bool {
	if url == "" {
		return false
	}
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		return false
	}
	return true
}
