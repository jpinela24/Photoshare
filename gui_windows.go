//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	wvWindow webview.WebView
	rootHwnd uintptr
	icoPath  string
)

// writeIconFile renders the app icon once into the temp dir as a .ico, for
// the native window/taskbar icon — reuses the same glyph as the PWA icons.
func writeIconFile() {
	icoPath = filepath.Join(os.TempDir(), "photoshare_icon.ico")
	if err := os.WriteFile(icoPath, wrapPNGAsICO(appIconPNG(64)), 0644); err != nil {
		log.Println("icon: write ico:", err)
	}
}

// runGUI creates the native Win32 window (via WebView2) and blocks until the
// process is asked to quit. Must run on the OS main thread.
func runGUI(url string) {
	writeIconFile()

	w := webview.New(false)
	if w == nil {
		log.Println("gui: WebView2 runtime unavailable, running headless (install Microsoft Edge / WebView2 runtime)")
		select {}
	}
	defer w.Destroy()

	wvWindow = w
	w.SetTitle("PhotoShare v" + appVersion)
	w.SetSize(1280, 820, webview.HintNone)
	w.Navigate(url)

	w.Dispatch(func() {
		root, _, _ := procGetAncestor.Call(uintptr(w.Window()), gaRoot)
		if root == 0 {
			root = uintptr(w.Window())
		}
		rootHwnd = root
		interceptWMClose(root)
		applyWindowIcon(root)
	})

	w.Run()
}

// showWindow is called from the /api/show HTTP handler to restore and focus
// the native window. Safe to call from any goroutine.
func showWindow() {
	if wvWindow == nil {
		return
	}
	wvWindow.Dispatch(func() {
		showWin(rootHwnd, swShow)
		showWin(rootHwnd, swRestore)
		setForegroundWindow.Call(rootHwnd)
	})
}

// applyWindowIcon assigns the generated .ico as both the window icon (used
// for the taskbar/Alt+Tab) and the window-class icon.
func applyWindowIcon(hwnd uintptr) {
	if icoPath == "" {
		return
	}
	pathPtr, err := windows.UTF16PtrFromString(icoPath)
	if err != nil {
		return
	}
	hicon, _, _ := procLoadImage.Call(
		0, uintptr(unsafe.Pointer(pathPtr)),
		imageIcon, 0, 0,
		lrLoadFromFile|lrDefaultSize,
	)
	if hicon == 0 {
		log.Println("gui: failed to load window icon from", icoPath)
		return
	}
	procSendMessage.Call(hwnd, wmSetIcon, iconBig, hicon)
	procSendMessage.Call(hwnd, wmSetIcon, iconSmall, hicon)
	procSetClassLongPtr.Call(hwnd, gclpHicon, hicon)
	procSetClassLongPtr.Call(hwnd, gclpHiconsm, hicon)
}

// acquireSingleInstanceLock claims a named OS mutex so only one PhotoShare
// process can run at a time. Returns false if another instance already holds
// it, in which case the caller should back off rather than start a second
// server/window/tray.
func acquireSingleInstanceLock() bool {
	namePtr, err := windows.UTF16PtrFromString(`Local\PhotoShareSingleInstanceMutex`)
	if err != nil {
		return true // fail open — better a rare duplicate than refusing to start
	}
	h, _, _ := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return true
	}
	return windows.GetLastError() != windows.ERROR_ALREADY_EXISTS
}

// restartProcess re-execs the binary with the same arguments, then exits —
// there's no supervisor on Windows to bring the process back up like Docker
// or systemd, so PhotoShare has to relaunch itself after a settings/setup
// save. It also tears down the tray's PowerShell process first so the new
// instance's tray icon doesn't end up duplicated.
func restartProcess() {
	stopTray()
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	suppressConsoleWindow(cmd)
	cmd.Start()
	os.Exit(0)
}

// suppressConsoleWindow stops a spawned child process from ever showing a
// console window (used for both the tray PowerShell and self-relaunch).
func suppressConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// ── Autostart (Task Scheduler, login trigger) ───────────────────────────────

const autostartTaskName = "PhotoShare"

func autostartEnabled() bool {
	cmd := exec.Command("schtasks", "/query", "/tn", autostartTaskName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run() == nil
}

func setAutostart(enabled bool) error {
	if !enabled {
		cmd := exec.Command("schtasks", "/delete", "/tn", autostartTaskName, "/f")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "cannot find") {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("schtasks", "/create", "/tn", autostartTaskName,
		"/tr", `"`+exe+`"`, "/sc", "onlogon", "/rl", "limited", "/f")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- raw user32.dll bindings (golang.org/x/sys/windows doesn't wrap these) ---

const (
	swShow    = 5
	swRestore = 9

	wmClose = 0x0010

	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4, two's complement

	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040

	gclpHicon   = ^uintptr(13) // GCLP_HICON   = -14
	gclpHiconsm = ^uintptr(33) // GCLP_HICONSM = -34

	gaRoot = 2 // GA_ROOT
)

var (
	modUser32           = windows.NewLazySystemDLL("user32.dll")
	procShowWindow      = modUser32.NewProc("ShowWindow")
	setForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procSetWindowLong   = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProc  = modUser32.NewProc("CallWindowProcW")
	procSendMessage     = modUser32.NewProc("SendMessageW")
	procSetClassLongPtr = modUser32.NewProc("SetClassLongPtrW")
	procLoadImage       = modUser32.NewProc("LoadImageW")
	procGetAncestor     = modUser32.NewProc("GetAncestor")

	modKernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutex = modKernel32.NewProc("CreateMutexW")

	origWndProc uintptr
)

func showWin(hwnd uintptr, cmd uintptr) {
	procShowWindow.Call(hwnd, cmd)
}

// --- WM_CLOSE intercept: hides the window instead of closing it, so closing
// the window minimizes to the tray rather than quitting the app -----

func interceptWMClose(hwnd uintptr) {
	cb := windows.NewCallback(wndProc)
	origWndProc, _, _ = procSetWindowLong.Call(hwnd, gwlpWndProc, cb)
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if msg == wmClose {
		showWin(hwnd, 0 /* SW_HIDE */)
		return 0
	}
	ret, _, _ := procCallWindowProc.Call(origWndProc, hwnd, msg, wParam, lParam)
	return ret
}

// wrapPNGAsICO wraps a single PNG as a minimal .ico container. Windows
// Vista+ supports PNG-compressed images inside ICO files directly, so no
// pixel-format conversion is needed.
func wrapPNGAsICO(png []byte) []byte {
	var buf []byte
	// ICONDIR: reserved(2)=0, type(2)=1 (icon), count(2)=1
	buf = append(buf, 0, 0, 1, 0, 1, 0)
	const headerLen = 6 + 16
	size := uint32(len(png))
	offset := uint32(headerLen)
	entry := []byte{
		64, 64, // width, height
		0,    // color count
		0,    // reserved
		1, 0, // planes
		32, 0, // bit count
		byte(size), byte(size >> 8), byte(size >> 16), byte(size >> 24),
		byte(offset), byte(offset >> 8), byte(offset >> 16), byte(offset >> 24),
	}
	buf = append(buf, entry...)
	buf = append(buf, png...)
	return buf
}
