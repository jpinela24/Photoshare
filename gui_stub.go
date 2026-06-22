//go:build !windows

package main

import (
	"errors"
	"os"
)

// acquireSingleInstanceLock is a no-op everywhere except Windows.
func acquireSingleInstanceLock() bool { return true }

// runGUI blocks forever on non-Windows — equivalent to the old bare
// `select {}` at the end of main(). The HTTP server and background workers
// keep running in their own goroutines.
func runGUI(url string) {
	select {}
}

// showWindow is a no-op outside the Windows native-window build.
func showWindow() {}

// restartProcess exits so the container/service supervisor (Docker
// `restart: unless-stopped`, systemd, etc.) restarts us with the new config —
// identical to the previous unconditional os.Exit(0) behavior.
func restartProcess() {
	os.Exit(0)
}

// autostartEnabled/setAutostart are Windows-only features.
func autostartEnabled() bool          { return false }
func setAutostart(enabled bool) error { return errors.New("not supported on this platform") }
