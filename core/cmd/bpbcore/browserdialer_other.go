//go:build !windows

// The browser dialer on non-Windows platforms: only Xray's side is switched; the integration
// tests run in proxy mode without it.
//
package main

import xray "bpb/core/xray"

const browserDialerAddress = "127.0.0.1:10832"

func startBrowserDialer(address string) { xray.SetBrowserDialer(address) }

func stopBrowserDialer() { xray.SetBrowserDialer("") }
