//go:build windows

// The system HTTP proxy (appendHttpProxy on Windows): while connected, WinINET's proxy —
// what most Windows apps use — points at Xray's mixed inbound; Android sets the same proxy
// on the VPN network. The change is broadcast with InternetSetOption so it applies at once.
//
package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

// setSystemProxy turns the WinINET proxy on ("127.0.0.1:port") or off. Only a proxy this
// host set is cleared.
func setSystemProxy(enable bool, port int) string {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Sprintf("the system proxy could not be changed: %v", err)
	}
	defer key.Close()

	if enable {
		if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
			return err.Error()
		}
		if err := key.SetStringValue("ProxyServer", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			return err.Error()
		}
	} else {
		current, _, _ := key.GetStringValue("ProxyServer")
		if current == "" || current == fmt.Sprintf("127.0.0.1:%d", port) {
			if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
				return err.Error()
			}
			key.DeleteValue("ProxyServer")
		}
	}
	refreshSystemProxy()
	return ""
}

// refreshSystemProxy tells WinINET the settings changed (no restart needed).
func refreshSystemProxy() {
	wininet := syscall.NewLazyDLL("wininet.dll")
	internetSetOption := wininet.NewProc("InternetSetOptionW")
	internetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	internetSetOption.Call(0, internetOptionRefresh, 0, 0)
}

// clearSystemProxy is the deferred clear on disconnect; errors only matter while connected.
func clearSystemProxy() {
	_ = setSystemProxy(false, localProxyPort)
}

var _ = unsafe.Pointer(nil)
