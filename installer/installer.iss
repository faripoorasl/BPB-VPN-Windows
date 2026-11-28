; The BPB VPN Windows installer (Inno Setup). The CI passes the portable bundle below and
; builds BPB-VPN-Setup-<version>-windows-x64.exe with ISCC.exe.

#define MyAppName "BPB VPN"
#ifndef MyAppVersion
#define MyAppVersion "1.3.2"
#endif

[Setup]
AppId={{5D7965D1-47DE-4380-8BE8-D5087B4930B3}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher=BPB
DefaultDirName={autopf}\BPB VPN
DefaultGroupName={#MyAppName}
UninstallDisplayName={#MyAppName}
UninstallDisplayIcon={app}\BPB-VPN.exe
OutputDir=Output
OutputBaseFilename=BPB-VPN-Setup-{#MyAppVersion}-windows-x64
; The portable bundle's files are staged here by the CI.
SourceDir=..\build\staged
Compression=lzma2/max
SolidCompression=yes
ArchitecturesInstallIn64BitMode=x64compatible
ArchitecturesAllowed=x64compatible
WizardStyle=modern
PrivilegesRequired=admin
UsedUserAreasWarning=no
SetupIconFile=..\..\windows\runner\resources\app_icon.ico
MinVersion=10.0

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "*"; DestDir: "{app}"; Flags: recursesubdirs ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\BPB-VPN.exe"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\BPB-VPN.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Run]
Filename: "{app}\BPB-VPN.exe"; Description: "{cm:LaunchProgram,{#MyAppName}}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; The service's data (logs, settings cache) goes with the app.
Type: filesandordirs; Name: "{localappdata}\BPBVPN"
