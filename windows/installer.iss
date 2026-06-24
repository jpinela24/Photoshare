; Inno Setup script for PhotoShare (Windows desktop build).
; Build the binary first: `make build-windows` (from the repo root), which
; produces photoshare.exe — then compile this script with Inno Setup
; (iscc windows\installer.iss) to produce windows\Output\PhotoShareSetup.exe.
;
; Re-running this installer for a later version overwrites photoshare.exe in
; place; config/data stays in %APPDATA%\PhotoShare, untouched by the install.

#define MyAppName "PhotoShare"
#define MyAppVersion "2.4.2"
#define MyAppPublisher "jpinela24"
#define MyAppURL "https://github.com/jpinela24"
#define MyAppExeName "photoshare.exe"

[Setup]
; Fixed GUID so future installer versions are recognized as upgrades of the
; same app (shows in Add/Remove Programs as one entry, not duplicates).
AppId={{B4B6E4B0-7B0C-4C3D-9B6E-1A2B3C4D5E6F}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=Output
OutputBaseFilename=PhotoShareSetup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExeName}
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"
Name: "firewall"; Description: "Allow PhotoShare through the Windows Firewall (needed for other devices on your network to connect)"; GroupDescription: "Network:"

[Files]
; Built by `make build-windows` from the repo root before compiling this script.
Source: "..\photoshare.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "netsh"; Parameters: "advfirewall firewall add rule name=""PhotoShare"" dir=in action=allow program=""{app}\{#MyAppExeName}"" enable=yes"; \
    Flags: runhidden; Tasks: firewall
Filename: "{app}\{#MyAppExeName}"; Description: "Launch {#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; Kill the app; the tray's watchdog (see buildTrayScript) notices the PID is
; gone within ~1.5s and disposes its own icon cleanly via the script's
; Remove-Tray/finally path — so the tray never lingers. We deliberately do
; NOT force-kill the tray PowerShell here: an external Stop-Process would
; terminate it before its finally block runs, orphaning the NotifyIcon and
; leaving exactly the "ghost icon until hover" this design avoids.
Filename: "taskkill"; Parameters: "/IM {#MyAppExeName} /F"; Flags: runhidden
Filename: "schtasks"; Parameters: "/delete /tn PhotoShare /f"; Flags: runhidden
Filename: "netsh"; Parameters: "advfirewall firewall delete rule name=""PhotoShare"""; Flags: runhidden
