; Inno Setup script for the Limulus (PicoClaw fork) Windows installer.
;
; Build (from repo root, after building build\picoclaw.exe and
; build\picoclaw-launcher.exe):
;   iscc /DMyAppVersion=<git describe> scripts\setup.iss
;
; Output: build\LimulusSetup-<version>.exe

; Override from the ISCC command line with /DMyAppVersion=...
#ifndef MyAppVersion
  #define MyAppVersion "0.0.0-dev"
#endif

#define MyAppName "Limulus Launcher"
#define MyAppPublisher "Limulus"
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
DefaultDirName={autopf}\Limulus
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
; Always show the "Select Destination Location" page. On upgrades the default
; is pre-filled with the previous install dir (UsePreviousAppDir), but the
; user must still be able to change it.
DisableDirPage=no
UsePreviousAppDir=yes
; Process shutdown is handled explicitly in [Code] PrepareToInstall with a
; confirmation dialog: Restart Manager is unreliable for the windowless
; background gateway process and its cryptic prompts abort installs.
CloseApplications=no
; Remove the following line to run in administrative install mode (install for all users.)
PrivilegesRequired=lowest
OutputDir=..\build
OutputBaseFilename=LimulusSetup-{#MyAppVersion}
Compression=lzma
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\web\backend\icon.ico
; Auto-pick the wizard language from the Windows UI language; when no
; translation matches, Inno falls back to the FIRST [Languages] entry.
; Keep English first. Set to yes to always show a language picker instead.
ShowLanguageDialog=no

[Languages]
; English MUST stay first: it is the fallback when the Windows UI language
; matches no translation here (e.g. zh-TW systems fall back to English).
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "zhcn"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"

[CustomMessages]
; Strings referenced from [Code] via CustomMessage(); en.* doubles as the
; fallback wording, zhcn.* is the Simplified Chinese override. %n = newline,
; %1/%2 are FmtMessage() substitution slots.
en.RunningProcessesPrompt=Limulus is currently running (picoclaw.exe / picoclaw-launcher.exe).%nThese processes must be stopped to complete the upgrade.%n%nStop them now?
en.RunningProcessesStopFailed=Could not fully stop the Limulus processes. Please close them manually, then click "Retry".
en.RunningProcessesAborted=Limulus processes are still running, so Setup cannot continue. Please stop picoclaw.exe and picoclaw-launcher.exe manually and run Setup again.
en.UpgradeDetected=Detected installed version %1. This will upgrade to %2.
zhcn.RunningProcessesPrompt=检测到 Limulus 正在运行（picoclaw.exe / picoclaw-launcher.exe），%n需要停止它们才能完成升级安装。%n%n是否现在停止这些进程？
zhcn.RunningProcessesStopFailed=未能完全停止 Limulus 进程，请手动关闭后点击"重试"。
zhcn.RunningProcessesAborted=Limulus 进程仍在运行，无法继续安装。请手动停止 picoclaw.exe 和 picoclaw-launcher.exe 后重新运行安装程序。
zhcn.UpgradeDetected=检测到已安装版本 %1，本次将升级到 %2。

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

[Code]
const
  // Same key Inno writes for PrivilegesRequired=lowest installs; used to
  // detect a previous installation and show upgrade info on the Ready page.
  // Keep in sync with AppId in [Setup].
  PrevUninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{C8A1B4E7-D5F9-4C2A-8A6E-5F4D3C2A1B0E}_is1';

function PicoclawProcessRunning(const ExeName: string): Boolean;
var
  ResultCode: Integer;
begin
  // tasklist + find exits 0 when a matching process exists.
  Result := Exec(ExpandConstant('{cmd}'),
    '/C tasklist /FI "IMAGENAME eq ' + ExeName + '" | find /I "' + ExeName + '" > nul',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

function StopPicoclawProcesses(): Boolean;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM picoclaw.exe /F > nul 2>&1',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM picoclaw-launcher.exe /F > nul 2>&1',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Sleep(1500);
  Result := (not PicoclawProcessRunning('picoclaw.exe')) and
    (not PicoclawProcessRunning('picoclaw-launcher.exe'));
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Attempts: Integer;
begin
  Result := '';
  if (not PicoclawProcessRunning('picoclaw.exe')) and
     (not PicoclawProcessRunning('picoclaw-launcher.exe')) then
    exit;

  for Attempts := 1 to 3 do
  begin
    if MsgBox(CustomMessage('RunningProcessesPrompt'),
        mbConfirmation, MB_YESNO) = IDYES then
    begin
      if StopPicoclawProcesses() then
        exit;
      MsgBox(CustomMessage('RunningProcessesStopFailed'), mbError, MB_OK);
    end
    else
    begin
      // User declined: if they are installing into a fresh directory the
      // running processes (which lock the OLD install dir) may not conflict;
      // let file copying surface a retry prompt only if it really clashes.
      exit;
    end;
  end;

  Result := CustomMessage('RunningProcessesAborted');
end;

function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo,
  MemoTypeInfo, MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
var
  PrevVersion: String;
begin
  if RegQueryStringValue(HKCU, PrevUninstallKey, 'DisplayVersion', PrevVersion) and
     (PrevVersion <> '{#MyAppVersion}') then
    Result := FmtMessage(CustomMessage('UpgradeDetected'), [PrevVersion, '{#MyAppVersion}']) + NewLine + NewLine
  else
    Result := '';
  Result := Result + MemoDirInfo + NewLine + MemoGroupInfo + NewLine + MemoTasksInfo;
end;
