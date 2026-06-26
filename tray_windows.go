//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// startTray writes the icon PNG and a PowerShell script to %TEMP%, then
// launches PowerShell with the script. The tray's menu calls back into this
// process's own HTTP API: /api/show to open the native window, /api/quit to
// stop it. No third-party Go systray library is needed.
func startTray(url string) {
	tmp := os.TempDir()
	iconPath := filepath.Join(tmp, "photoshare_icon.png")
	if err := os.WriteFile(iconPath, appIconPNG(64), 0644); err != nil {
		log.Println("tray: write icon png:", err)
	}

	script := buildTrayScript(url, iconPath, os.Getpid())
	scriptPath := filepath.Join(tmp, "photoshare_tray.ps1")
	bom := []byte{0xEF, 0xBB, 0xBF} // UTF-8 BOM so PowerShell reads as UTF-8
	if err := os.WriteFile(scriptPath, append(bom, []byte(script)...), 0644); err != nil {
		log.Println("tray: write script:", err)
		return
	}

	cmd := exec.Command("powershell",
		"-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden",
		"-NonInteractive",
		"-File", scriptPath,
	)
	suppressConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Println("tray: failed to start powershell:", err)
		return
	}
	log.Println("tray: powershell started, PID", cmd.Process.Pid)

	go func() {
		err := cmd.Wait()
		log.Println("tray: powershell exited:", err)
	}()
}

// buildTrayScript renders the PowerShell tray. parentPID is this PhotoShare
// process's PID; the script self-terminates (and disposes its icon) as soon
// as that process is gone, so the tray never outlives the app — whether it
// exits via Quit, a crash, Task Manager, or the uninstaller killing the exe.
func buildTrayScript(url, iconPath string, parentPID int) string {
	iconPath = strings.ReplaceAll(iconPath, `\`, `\\`)

	return fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$url       = '%s'
$parentPid = %d

$bmp  = [System.Drawing.Bitmap]::FromFile('%s')
$icon = [System.Drawing.Icon]::FromHandle($bmp.GetHicon())

$ni         = New-Object System.Windows.Forms.NotifyIcon
$ni.Icon    = $icon
$ni.Text    = "PhotoShare"
$ni.Visible = $true

# Dispose the tray icon exactly once, from wherever the script unwinds, so
# Windows reaps it immediately instead of leaving a ghost until mouse-over.
$script:cleaned = $false
function Remove-Tray {
    if ($script:cleaned) { return }
    $script:cleaned = $true
    $ni.Visible = $false
    $ni.Dispose()
    [System.Windows.Forms.Application]::Exit()
}

function Show-Window {
    try {
        Invoke-WebRequest "$url/api/show" -Method POST -UseBasicParsing -TimeoutSec 3 | Out-Null
    } catch {
        Start-Process $url
    }
}

# Watchdog: when the PhotoShare process is gone (Quit, crash, Task Manager,
# or the uninstaller killing the exe), tear the tray down so it never lingers.
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 1500
$timer.add_Tick({
    if (-not (Get-Process -Id $parentPid -ErrorAction SilentlyContinue)) {
        Remove-Tray
    }
})
$timer.Start()

$cm = New-Object System.Windows.Forms.ContextMenuStrip

$mApp = New-Object System.Windows.Forms.ToolStripMenuItem('Open PhotoShare')
$mApp.Font = New-Object System.Drawing.Font($mApp.Font, [System.Drawing.FontStyle]::Bold)
$mApp.add_Click({ Show-Window })
$cm.Items.Add($mApp) | Out-Null

$mBrowser = New-Object System.Windows.Forms.ToolStripMenuItem("Open in browser  ($url)")
$mBrowser.add_Click({ Start-Process $url })
$cm.Items.Add($mBrowser) | Out-Null

$mCopy = New-Object System.Windows.Forms.ToolStripMenuItem('Copy URL')
$mCopy.add_Click({ Set-Clipboard $url })
$cm.Items.Add($mCopy) | Out-Null

$cm.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator)) | Out-Null

$mQuit = New-Object System.Windows.Forms.ToolStripMenuItem('Quit PhotoShare')
$mQuit.add_Click({
    try { Invoke-WebRequest "$url/api/quit" -Method POST -UseBasicParsing -TimeoutSec 2 | Out-Null } catch {}
    Remove-Tray
})
$cm.Items.Add($mQuit) | Out-Null

$ni.add_MouseClick({
    param($sender, $e)
    if ($e.Button -eq [System.Windows.Forms.MouseButtons]::Left) {
        Show-Window
    } elseif ($e.Button -eq [System.Windows.Forms.MouseButtons]::Right) {
        $cm.Show([System.Windows.Forms.Cursor]::Position)
    }
})

# finally guarantees disposal no matter how Run() unwinds.
try {
    [System.Windows.Forms.Application]::Run()
} finally {
    Remove-Tray
}
`, url, parentPID, iconPath)
}
