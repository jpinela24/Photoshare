//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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

	// Make the window behave like a native app rather than a browser. The
	// right-click context menu and devtools are already disabled by the
	// library (Debug=false above); this additionally suppresses the zoom
	// gestures (Ctrl+wheel, Ctrl +/-/0) and reload shortcuts (F5, Ctrl+R)
	// that otherwise let the UI be zoomed or reloaded like a web page. Init
	// runs before page scripts on every navigation.
	w.Init(`(function(){
  window.addEventListener('wheel', function(e){ if (e.ctrlKey) e.preventDefault(); }, { passive: false });
  window.addEventListener('keydown', function(e){
    var k = e.key;
    if (e.ctrlKey && (k === '+' || k === '-' || k === '=' || k === '0')) { e.preventDefault(); return; }
    if (k === 'F5' || (e.ctrlKey && (k === 'r' || k === 'R'))) { e.preventDefault(); }
  });
})();`)

	w.Navigate(url)

	w.Dispatch(func() {
		root, _, _ := procGetAncestor.Call(uintptr(w.Window()), gaRoot)
		if root == 0 {
			root = uintptr(w.Window())
		}
		rootHwnd = root
		interceptWMClose(root)
		applyWindowIcon(root)
		applyDarkTitleBar(root)
		restoreWindowGeometry(root)
	})

	// Persist the window's size/position so it reopens where the user left it.
	go windowGeometrySaver()

	w.Run()
}

// windowGeometry is the persisted window rectangle (screen coordinates).
type windowGeometry struct {
	X, Y, W, H int32
}

type winRect struct{ Left, Top, Right, Bottom int32 }

func geometryPath() string { return filepath.Join(dataDir, "window.json") }

// saveWindowGeometry writes the current window rect to disk, skipping bogus
// values (minimized windows report off-screen coordinates and tiny sizes).
func saveWindowGeometry() {
	if rootHwnd == 0 {
		return
	}
	var r winRect
	ret, _, _ := procGetWindowRect.Call(rootHwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return
	}
	g := windowGeometry{X: r.Left, Y: r.Top, W: r.Right - r.Left, H: r.Bottom - r.Top}
	if g.W < 300 || g.H < 300 || g.X < -10000 || g.Y < -10000 {
		return // minimized or garbage — don't clobber a good saved value
	}
	if data, err := json.Marshal(g); err == nil {
		os.WriteFile(geometryPath(), data, 0644)
	}
}

// restoreWindowGeometry moves/resizes the window to the last saved rect, if any.
func restoreWindowGeometry(hwnd uintptr) {
	data, err := os.ReadFile(geometryPath())
	if err != nil {
		return
	}
	var g windowGeometry
	if json.Unmarshal(data, &g) != nil || g.W < 300 || g.H < 300 {
		return
	}
	procSetWindowPos.Call(hwnd, 0,
		uintptr(uint32(g.X)), uintptr(uint32(g.Y)),
		uintptr(uint32(g.W)), uintptr(uint32(g.H)),
		uintptr(swpNoZOrder|swpNoActivate))
}

// windowGeometrySaver periodically persists the window rect so size/position
// survive a quit even though there's no clean shutdown hook (the tray's Quit
// calls os.Exit). The close-to-tray path also saves immediately (see wndProc).
func windowGeometrySaver() {
	for {
		time.Sleep(3 * time.Second)
		saveWindowGeometry()
	}
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

// applyDarkTitleBar makes the native Win32 title bar dark so it matches the
// app's dark UI instead of showing the default light caption. Two DWM calls,
// both no-ops (harmlessly ignored) on Windows versions that don't support
// them:
//   - immersive dark mode → light caption text + dark min/max/close buttons
//     (Win10 2004+/Win11; attribute was 19 on early Win10 builds, 20 since).
//   - caption color → paint the bar the app's exact header color #202124
//     (Win11 22H2+).
func applyDarkTitleBar(hwnd uintptr) {
	enabled := int32(1)
	// Try the modern attribute (20); fall back to the pre-20H1 value (19).
	ret, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode,
		uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
	if ret != 0 {
		procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld,
			uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
	}
	// COLORREF is 0x00BBGGRR; app header #202124 → R=20 G=21 B=24.
	caption := uint32(0x00242120)
	procDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor,
		uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))
}

// acquireSingleInstanceLock claims a named OS mutex so only one PhotoShare
// process can run at a time. Returns false if another instance already holds
// it, in which case the caller should back off rather than start a second
// server/window/tray.
var singleInstanceMutex windows.Handle

func acquireSingleInstanceLock() bool {
	namePtr, err := windows.UTF16PtrFromString(`Local\PhotoShareSingleInstanceMutex`)
	if err != nil {
		return true // fail open — better a rare duplicate than refusing to start
	}
	h, _, _ := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return true
	}
	singleInstanceMutex = windows.Handle(h)
	return windows.GetLastError() != windows.ERROR_ALREADY_EXISTS
}

// releaseSingleInstanceLock closes our handle to the named mutex. When this
// is the only handle, the mutex is destroyed immediately, so a relaunched
// copy can claim it right away instead of seeing it as "already running".
func releaseSingleInstanceLock() {
	if singleInstanceMutex != 0 {
		windows.CloseHandle(singleInstanceMutex)
		singleInstanceMutex = 0
	}
}

// restartProcess re-execs the binary, then exits — there's no supervisor on
// Windows to bring the process back up like Docker or systemd, so PhotoShare
// relaunches itself after a settings/setup save. The old tray icon cleans
// itself up once this process exits (its watchdog notices the PID is gone),
// and the relaunched instance spawns a fresh tray.
//
// Two things make the handoff reliable: we release the single-instance mutex
// *before* spawning the child (so it doesn't see us as a running instance and
// immediately back off — the bug that left nothing running after first-run
// setup), and we tag the child with PHOTOSHARE_RESTART so it skips the
// already-running check even if the mutex lingers a moment. If the spawn
// fails we reclaim the lock and keep running rather than exiting into nothing.
func restartProcess() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}
	releaseSingleInstanceLock()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "PHOTOSHARE_RESTART=1")
	suppressConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Println("restart: relaunch failed, keeping current instance:", err)
		acquireSingleInstanceLock() // reclaim so we remain the single instance
		return
	}
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

	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	// DWM window attributes for the dark title bar.
	dwmwaUseImmersiveDarkModeOld = 19 // DWMWA_USE_IMMERSIVE_DARK_MODE (pre-20H1)
	dwmwaUseImmersiveDarkMode    = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE (20H1+)
	dwmwaCaptionColor            = 35 // DWMWA_CAPTION_COLOR (Win11 22H2+)
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
	procGetWindowRect   = modUser32.NewProc("GetWindowRect")
	procSetWindowPos    = modUser32.NewProc("SetWindowPos")

	modKernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutex = modKernel32.NewProc("CreateMutexW")

	modDwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")

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
		saveWindowGeometry() // remember where it was before hiding to the tray
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
