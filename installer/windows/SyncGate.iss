#ifndef AppVersion
  #error AppVersion must be provided with /DAppVersion=x.y.z
#endif
#ifndef SourceDir
  #error SourceDir must point to the signed Windows release artifacts
#endif

#define AppName "SyncGate Desktop Node"
#define AppPublisher "SyncGate"
#define AppExeName "syncgate.exe"

[Setup]
AppId={{7D984546-06A5-4E24-B6D2-856605569775}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher={#AppPublisher}
DefaultDirName={localappdata}\Programs\SyncGate
DefaultGroupName=SyncGate
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=.
OutputBaseFilename=SyncGate-{#AppVersion}-windows-amd64-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no
SetupLogging=yes
UninstallDisplayIcon={app}\{#AppExeName}
InfoAfterFile=uninstall-policy.txt
VersionInfoVersion={#AppVersion}
VersionInfoProductName={#AppName}
VersionInfoProductVersion={#AppVersion}
VersionInfoCompany={#AppPublisher}

[Files]
Source: "{#SourceDir}\syncgate.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\version-manifest.json"; DestDir: "{app}"; Flags: ignoreversion
Source: "uninstall-policy.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Initialize SyncGate Node"; Filename: "{app}\{#AppExeName}"; Parameters: "node-init"; WorkingDir: "{app}"
Name: "{group}\Run SyncGate Node"; Filename: "{app}\{#AppExeName}"; Parameters: "node-run"; WorkingDir: "{app}"
Name: "{group}\Check SyncGate Health"; Filename: "{app}\{#AppExeName}"; Parameters: "node-health"; WorkingDir: "{app}"
Name: "{group}\Uninstall SyncGate"; Filename: "{uninstallexe}"

[Run]
Filename: "{app}\{#AppExeName}"; Parameters: "node-init"; Description: "Initialize per-user node state"; Flags: runhidden waituntilterminated skipifsilent
Filename: "{app}\{#AppExeName}"; Parameters: "node-run"; Description: "Start SyncGate in foreground mode"; Flags: nowait postinstall skipifsilent unchecked

[Registry]
Root: HKCU; Subkey: "Software\SyncGate\Installer"; ValueType: string; ValueName: "Version"; ValueData: "{#AppVersion}"; Flags: uninsdeletevalue
Root: HKCU; Subkey: "Software\SyncGate\Installer"; ValueType: dword; ValueName: "ControlLayoutVersion"; ValueData: "1"; Flags: uninsdeletevalue

; Deliberately no [UninstallDelete] section. Inno Setup removes only files it
; installed below {app}. Config, SQLite/control state, logs, cache, registered
; projects, and worktrees live elsewhere and remain after uninstall.

[Code]
function NextVersionPart(var Value: String): Integer;
var
  Separator: Integer;
  Part: String;
begin
  Separator := Pos('.', Value);
  if Separator = 0 then
  begin
    Part := Value;
    Value := '';
  end
  else
  begin
    Part := Copy(Value, 1, Separator - 1);
    Delete(Value, 1, Separator);
  end;
  Separator := Pos('-', Part);
  if Separator > 0 then
    Part := Copy(Part, 1, Separator - 1);
  Result := StrToIntDef(Part, 0);
end;

function CompareSemanticVersion(Left, Right: String): Integer;
var
  Index: Integer;
  LeftPart: Integer;
  RightPart: Integer;
begin
  Result := 0;
  for Index := 1 to 3 do
  begin
    LeftPart := NextVersionPart(Left);
    RightPart := NextVersionPart(Right);
    if LeftPart < RightPart then
    begin
      Result := -1;
      exit;
    end;
    if LeftPart > RightPart then
    begin
      Result := 1;
      exit;
    end;
  end;
end;

function InitializeSetup(): Boolean;
var
  InstalledVersion: String;
  AllowDowngrade: String;
begin
  Result := True;
  AllowDowngrade := ExpandConstant('{param:ALLOWDOWNGRADE|0}');
  if RegQueryStringValue(HKCU, 'Software\SyncGate\Installer', 'Version', InstalledVersion) and
     (CompareSemanticVersion('{#AppVersion}', InstalledVersion) < 0) and
     (AllowDowngrade <> '1') then
  begin
    MsgBox('A newer SyncGate version (' + InstalledVersion + ') is installed. ' +
      'Downgrades are blocked by default. Use /ALLOWDOWNGRADE=1 only after ' +
      'confirming the embedded control-layout compatibility window.', mbError, MB_OK);
    Result := False;
  end;
end;
