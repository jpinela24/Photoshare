//go:build !windows

package main

// startTray is a no-op outside the Windows build — Linux/Docker has no tray.
func startTray(url string) {}
