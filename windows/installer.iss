; Inno Setup script for PhotoShare (Windows desktop build).
; Build the binary first: `make build-windows` (from the repo root), which
; produces photoshare.exe — then compile this script with Inno Setup
; (iscc windows\installer.iss) to produce windows\Output\PhotoShareSetup.exe.
;
; Re-running this installer for a later version overwrites photoshare.exe in
; place; config/data stays in %APPDATA%\PhotoShare, untouched by the install.

#define MyAppName "PhotoShare"
#define MyAppVersion "2.14.0"
#define MyAppPublisher "jpinela24"
#define MyAppURL "https://github.com/jpinela24"
#define MyAppExeName "photoshare.exe"

; Architecture is passed in by the build (iscc /DAppArch=arm64 /DSetupName=…).
; Defaults to x64 so a bare `iscc windows\installer.iss` still works.
#ifndef AppArch
  #define AppArch "x64"
#endif
#ifndef SetupName
  #define SetupName "PhotoShareSetup"
#endif

[Setup]
; Fixed GUID so future installer versions are recognized as upgrades of the
; same app (shows in Add/Remove Programs as one entry, not duplicates). The
; same AppId is used for both architectures so an ARM machine that somehow
; had the x64 build installed upgrades cleanly to the ARM one.
AppId={{B4B6E4B0-7B0C-4C3D-9B6E-1A2B3C4D5E6F}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=Output
OutputBaseFilename={#SetupName}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\icon.ico
ArchitecturesAllowed={#AppArch}
ArchitecturesInstallIn64BitMode={#AppArch}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"
Name: "firewall"; Description: "Allow PhotoShare through the Windows Firewall (needed for other devices on your network to connect)"; GroupDescription: "Network:"

[Files]
; Built by `make build-windows` from the repo root before compiling this script.
Source: "..\photoshare.exe"; DestDir: "{app}"; Flags: ignoreversion
; Ship the icon alongside the exe so shortcuts can point at a real .ico file
; (more reliable than relying on the exe's embedded icon, and a reinstall
; rewrites the .lnk so Windows refreshes its cached shortcut icon).
Source: "icon.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\icon.ico"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\icon.ico"; Tasks: desktopicon

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
