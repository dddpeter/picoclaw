; Inno Setup script for the PicoClaw Windows installer.
;
; Build (from repo root, after building build\picoclaw.exe and
; build\picoclaw-launcher.exe):
;   iscc /DMyAppVersion=<git describe> scripts\setup.iss
;
; Output: build\PicoClawSetup-<version>.exe

; Override from the ISCC command line with /DMyAppVersion=...
#ifndef MyAppVersion
  #define MyAppVersion "0.0.0-dev"
#endif

#define MyAppName "PicoClaw Launcher"
#define MyAppPublisher "PicoClaw"
#define MyAppURL "https://github.com/sipeed/picoclaw"
#define MyAppExeName "picoclaw-launcher.exe"

[Setup]
; NOTE: The value of AppId uniquely identifies this application. Do not use the same AppId value in installers for other applications.
; (To generate a new GUID, click Tools | Generate GUID inside the IDE.)
AppId={{C8A1B4E7-D5F9-4C2A-8A6E-5F4D3C2A1B0E}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
DefaultDirName={autopf}\PicoClaw
DefaultGroupName={#MyAppName}
; "ArchitecturesAllowed=x64compatible" specifies that Setup cannot run
; on anything but x64 and Windows 11 on Arm.
ArchitecturesAllowed=x64compatible
; "ArchitecturesInstallIn64BitMode=x64compatible" requests that the
; install be done in "64-bit mode" on x64 or Windows 11 on Arm,
; meaning it should use the native 64-bit Program Files directory and
; the 64-bit view of the registry.
ArchitecturesInstallIn64BitMode=x64compatible
DisableProgramGroupPage=yes
; Remove the following line to run in administrative install mode (install for all users.)
PrivilegesRequired=lowest
OutputDir=..\build
OutputBaseFilename=PicoClawSetup-{#MyAppVersion}
Compression=lzma
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\web\backend\icon.ico

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\build\picoclaw-launcher.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}"; Flags: ignoreversion
Source: "..\build\picoclaw.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\web\backend\icon.ico"; DestDir: "{app}"; Flags: ignoreversion
; Reference copy of the default config, kept next to the binaries.
Source: "..\config\config.example.json"; DestDir: "{app}"; DestName: "config.example.json"; Flags: ignoreversion
; Seed the user config on first install only: never overwrite an existing
; config (it may already hold real API keys) and never delete it on uninstall.
Source: "..\config\config.example.json"; DestDir: "{%USERPROFILE}\.picoclaw"; DestName: "config.json"; Flags: onlyifdoesntexist uninsneveruninstall skipifsourcedoesntexist
; NOTE: Don't use "Flags: ignoreversion" on any shared system files

[UninstallDelete]

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; IconFilename: "{app}\icon.ico"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Tasks: desktopicon; IconFilename: "{app}\icon.ico"

[Run]
Filename:"{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Description: "{cm:LaunchProgram,{#StringChange(MyAppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent
