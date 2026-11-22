; Zatvor installer prototype, Inno Setup 6 (stage 0c, task 10).
; Machine-wide install into Program Files with an uninstall entry (spec
; section 16 Q2.9). Build:
;   ISCC.exe /DAppVersion=0.0.1 /DSrcDir=<dir with zatvor.exe> /O<outdir> zatvor.iss

#ifndef AppVersion
  #define AppVersion "0.0.1"
#endif
#ifndef SrcDir
  #define SrcDir "..\out"
#endif

[Setup]
; Fixed AppId: an install of a newer version replaces the older one in place
; and keeps a single "Apps & features" entry.
AppId={{937479BC-4AC5-463F-B8E6-A1459359920F}
AppName=Zatvor
AppVersion={#AppVersion}
AppVerName=Zatvor {#AppVersion}
AppPublisher=Zatvor project
AppPublisherURL=https://github.com/ArtikGenius/zatvor
VersionInfoVersion={#AppVersion}
DefaultDirName={autopf}\Zatvor
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
; Machine-wide: one UAC prompt, then everything runs elevated.
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayName=Zatvor
UninstallDisplayIcon={app}\zatvor.exe
OutputBaseFilename=zatvor-setup-{#AppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; Close the running app on upgrade/uninstall (Restart Manager).
CloseApplications=yes

[Files]
Source: "{#SrcDir}\zatvor.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SrcDir}\VERSION.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Zatvor"; Filename: "{app}\zatvor.exe"

; Stage 3 placeholder: the privileged helper service is added by this same
; installer. Sketch (not active in 0c):
;
; [Files]
; Source: "{#SrcDir}\zatvor-helper.exe"; DestDir: "{app}"; Flags: ignoreversion
;
; [Run]
; Filename: "{sys}\sc.exe"; Parameters: "create zatvor-helper binPath= ""{app}\zatvor-helper.exe"" start= auto"; Flags: runhidden
; Filename: "{sys}\sc.exe"; Parameters: "start zatvor-helper"; Flags: runhidden
;
; [UninstallRun]
; Filename: "{sys}\sc.exe"; Parameters: "stop zatvor-helper"; Flags: runhidden; RunOnceId: "StopHelper"
; Filename: "{sys}\sc.exe"; Parameters: "delete zatvor-helper"; Flags: runhidden; RunOnceId: "DeleteHelper"
;
; plus a [Code] PrepareToInstall step that stops the service before files
; are replaced on upgrade.
