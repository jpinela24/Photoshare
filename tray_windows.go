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

var trayCmd *exec.Cmd

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

	script := buildTrayScript(url, iconPath)
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
	trayCmd = cmd
	log.Println("tray: powershell started, PID", cmd.Process.Pid)

	go func() {
		err := cmd.Wait()
		log.Println("tray: powershell exited:", err)
	}()
}

// stopTray kills the tray's PowerShell process, if running — called before a
// self-relaunch (restartProcess) so the old tray icon doesn't linger
// alongside the new instance's icon.
func stopTray() {
	if trayCmd != nil && trayCmd.Process != nil {
		trayCmd.Process.Kill()
	}
}

func buildTrayScript(url, iconPath string) string {
	iconPath = strings.ReplaceAll(iconPath, `\`, `\\`)

	return fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$url = '%s'

$bmp  = [System.Drawing.Bitmap]::FromFile('%s')
$icon = [System.Drawing.Icon]::FromHandle($bmp.GetHicon())

$ni         = New-Object System.Windows.Forms.NotifyIcon
$ni.Icon    = $icon
$ni.Text    = "PhotoShare"
$ni.Visible = $true

function Show-Window {
    try {
        Invoke-WebRequest "$url/api/show" -UseBasicParsing -TimeoutSec 3 | Out-Null
    } catch {
        Start-Process $url
    }
}

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
    $ni.Visible = $false
    try { Invoke-WebRequest "$url/api/quit" -UseBasicParsing -TimeoutSec 2 | Out-Null } catch {}
    [System.Windows.Forms.Application]::Exit()
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

[System.Windows.Forms.Application]::Run()
`, url, iconPath)
}
