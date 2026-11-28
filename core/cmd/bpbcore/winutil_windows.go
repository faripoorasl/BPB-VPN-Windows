//go:build windows

// Windows helpers: elevation, the wintun driver and opening URLs.
//
package main

import (
        "os"
        "path/filepath"
        "syscall"
        "unsafe"

        "golang.org/x/sys/windows"
)

// isElevated: the process's token has administrator rights.
func isElevated() bool {
        return windows.GetCurrentProcessToken().IsElevated()
}

// wintunFound: the TUN driver is next to the binary or in the system dir.
func wintunFound(binDir string) bool {
        if _, err := os.Stat(filepath.Join(binDir, "wintun.dll")); err == nil {
                return true
        }
        sysDir := os.Getenv("SystemRoot")
        if sysDir == "" {
                sysDir = `C:\Windows`
        }
        if _, err := os.Stat(filepath.Join(sysDir, "System32", "wintun.dll")); err == nil {
                return true
        }
        return false
}

// openURL opens [url] in the default browser; false if nothing could handle it.
func openURL(url string) bool {
        if url == "" {
                return false
        }
        // ShellExecute "open" without spawning a console window.
        urlUTF16, err := syscall.UTF16PtrFromString(url)
        if err != nil {
                return false
        }
        shell32 := windows.NewLazySystemDLL("shell32.dll")
        shellExecute := shell32.NewProc("ShellExecuteW")
        ret, _, _ := shellExecute.Call(0, 0, uintptr(unsafe.Pointer(urlUTF16)), 0, 0, 1 /* SW_SHOWNORMAL */)
        return ret > 32
}
