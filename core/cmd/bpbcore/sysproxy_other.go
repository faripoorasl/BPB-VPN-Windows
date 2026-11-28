//go:build !windows

// The system proxy is a Windows feature; other platforms do nothing.
//
package main

func setSystemProxy(enable bool, port int) string { return "" }

func clearSystemProxy() {}
